package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"golang.org/x/oauth2/google"
)

// Cloud Run は既定で**リクエスト処理中しかCPUを割り当てない**。
//
// Discordへ「考えています」を返したあとの goroutine は、そのままでは実行が
// 保証されない（docs/09 A-11）。実機では動いているが、保証はされていない。
//
// そこで、回答を作る仕事を Cloud Tasks へ積み、**別のリクエストとして戻す**。
// 届いたリクエストの処理中はCPUが割り当てられるので、確実に最後まで走る。
//
// ⚠️ **`--no-cpu-throttling` は使わない。** 試算で月¥2,000〜2,500かかり、
// 「API以外は¥0」（docs/01 §7）と両立しない。Cloud Tasks は月100万回まで無料で、
// この用途では1日数十回しか使わない。
//
// TASKS_QUEUE が未設定なら、いままでどおり goroutine で動く。

// taskTimeout は積んだ仕事を処理する上限。
// Discordのトークンは15分で失効するので、それより手前で諦める。
const taskTimeout = 5 * time.Minute

// discordJob は Cloud Tasks 経由で運ぶ仕事。
//
// **interaction をそのまま運ばない。** 必要な値だけにすれば、
// Discordの形が変わってもここは壊れない。
type discordJob struct {
	Command     string `json:"command"`
	Question    string `json:"question"`
	AssistantID string `json:"assistantId"`
	Token       string `json:"token"`
	UserID      string `json:"userId"`
	Username    string `json:"username"`
	GuildID     string `json:"guildId"`
	ChannelID   string `json:"channelId"`
	Scope       string `json:"scope"`
	Days        int    `json:"days"`
}

// signJob は仕事に署名する。
//
// ⚠️ **この口は公開される。** Cloud Tasks から届いたことを確かめないと、
// 誰でも叩いて無料枠を消費させられる。Discordの署名と同じ考え方で、
// **こちらが出した仕事であること**を署名で示す。
func (s *Server) signJob(body []byte) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) verifyJob(body []byte, signature string) bool {
	return hmac.Equal([]byte(s.signJob(body)), []byte(signature))
}

// enqueueDiscordJob は仕事を Cloud Tasks へ積む。積めなければ false。
//
// **積む呼び出しは元のリクエストの中で終わる。** ここが速いので、
// 「考えています」を3秒以内に返すことと両立する。
func (s *Server) enqueueDiscordJob(ctx context.Context, job discordJob) bool {
	if s.cfg.TasksQueue == "" || s.cfg.PublicURL == "" {
		return false
	}
	body, err := json.Marshal(job)
	if err != nil {
		log.Printf("Discordの仕事を組み立てられません: %v", err)
		return false
	}
	payload := map[string]any{
		"task": map[string]any{
			"httpRequest": map[string]any{
				"httpMethod": http.MethodPost,
				"url":        s.cfg.PublicURL + "/api/discord/work",
				"headers": map[string]string{
					"Content-Type":      "application/json",
					discordJobSignature: s.signJob(body),
				},
				"body": base64.StdEncoding.EncodeToString(body),
			},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return false
	}

	client, err := google.DefaultClient(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		log.Printf("Cloud Tasks の資格情報を取れません: %v", err)
		return false
	}
	url := fmt.Sprintf("https://cloudtasks.googleapis.com/v2/%s/tasks", s.cfg.TasksQueue)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		log.Printf("Cloud Tasks へ積めません: %v", err)
		return false
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 256))
		log.Printf("Cloud Tasks が受け付けません（%d）: %s", res.StatusCode, detail)
		return false
	}
	return true
}

// discordJobSignature は署名を載せるヘッダ名。
const discordJobSignature = "X-Wasa-Job-Signature"

// handleDiscordWork は Cloud Tasks から届いた仕事を処理する。
//
// ⚠️ **必ず 200 を返す。** Cloud Tasks は 2xx 以外だと再試行するので、
// 失敗したときに 500 を返すと**同じ質問でLLMを何度も呼ぶ**ことになる。
// 無料枠を溶かすうえ、Discordのトークンは15分で失効するので再試行に意味が無い。
func (s *Server) handleDiscordWork(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, discordBodyLimit))
	if err != nil {
		http.Error(w, "読み取りに失敗しました", http.StatusBadRequest)
		return
	}
	// **この口は公開される。** 署名を見ないと誰でも叩ける
	if !s.verifyJob(body, r.Header.Get(discordJobSignature)) {
		http.Error(w, "署名が不正です", http.StatusUnauthorized)
		return
	}
	var job discordJob
	if err := json.Unmarshal(body, &job); err != nil {
		// 解釈できない仕事を再試行しても直らない。200 で捨てる
		log.Printf("Discordの仕事を解釈できません: %v", err)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": false})
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), taskTimeout)
	defer cancel()
	s.runDiscordJob(ctx, job)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
