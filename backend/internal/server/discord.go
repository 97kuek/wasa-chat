package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/97kuek/wasa-chat/backend/internal/discord"
	"github.com/97kuek/wasa-chat/backend/internal/llm"
	"github.com/97kuek/wasa-chat/backend/internal/pipeline"
)

// Discordからの対話を受ける。
//
// ⚠️ **ここは認証ミドルウェアを通さない。** Cookieではなく、Discordの署名で
// 本人確認する。代わりに署名の検証を外さないこと（外すと誰でも叩けて、
// 無料枠を好きなだけ消費させられる）。
//
// ⚠️ **誰が使えるかは Discord のサーバー参加者で決まる。** Wikiアカウントでは
// ないので、退部者を止めるには Discord 側から外す必要がある。つまり
// **失効の手段が2か所になる**（docs/09 A-6）。2026-09-13にPMが承認した運用。
const discordBodyLimit = 1 << 20

// discordAnswerTimeout は追いかけて書き換えるまでの上限。
// Discordのトークンは15分で失効するので、それより十分手前で諦める。
const discordAnswerTimeout = 5 * time.Minute

func (s *Server) handleDiscord(w http.ResponseWriter, r *http.Request) {
	if s.cfg.DiscordPublicKey == "" {
		http.NotFound(w, r)
		return
	}
	// **署名の対象は生バイト。** JSONへ解釈してから組み立て直すと一致しない
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, discordBodyLimit))
	if err != nil {
		http.Error(w, "読み取りに失敗しました", http.StatusBadRequest)
		return
	}
	if !discord.Verify(
		s.cfg.DiscordPublicKey,
		r.Header.Get("X-Signature-Ed25519"),
		r.Header.Get("X-Signature-Timestamp"),
		body,
	) {
		// Discordは登録時にわざと壊した署名を送ってきて、401を返せるかを試す
		http.Error(w, "署名が不正です", http.StatusUnauthorized)
		return
	}

	var interaction discord.Interaction
	if err := json.Unmarshal(body, &interaction); err != nil {
		http.Error(w, "リクエストが不正です", http.StatusBadRequest)
		return
	}

	switch interaction.Type {
	case discord.TypePing:
		writeRaw(w, discord.Pong())
	case discord.TypeApplicationCommand:
		s.startDiscordAnswer(&interaction)
		// **3秒以内に返す。** 実際の回答は後から書き換える
		writeRaw(w, discord.Deferred())
	default:
		writeRaw(w, discord.Pong())
	}
}

// startDiscordAnswer は回答を作って、最初の応答を書き換える。
//
// **元のリクエストの context を使わない。** Discordへ「考えています」を返した
// 時点でHTTPは終わるため、そのcontextは即座に切れる。回答は別の寿命で作る。
func (s *Server) startDiscordAnswer(interaction *discord.Interaction) {
	question := strings.TrimSpace(interaction.Question())
	userID := interaction.UserID()
	token := interaction.Token

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), discordAnswerTimeout)
		defer cancel()
		s.replyDiscord(ctx, token, s.answerForDiscord(ctx, question, userID, interaction.Username()))
	}()
}

func (s *Server) answerForDiscord(ctx context.Context, question, userID, username string) string {
	if question == "" {
		return "質問を入力してください。"
	}
	if len([]rune(question)) > maxQuestionRunes {
		return fmt.Sprintf("質問は%d文字以内にしてください。", maxQuestionRunes)
	}

	// 利用回数はDiscordの利用者ごとに数える。**Wikiの利用者とは別枠になる。**
	// 同じ人が画面とDiscordで別々に数えられるが、無料枠そのものは
	// 送信前の判定（GEMINI_RPD_LIMIT）が守る
	day := time.Now().In(japanTime).Format("2006-01-02")
	userKey := s.userKey("discord:" + userID)
	ok, err := s.state.Take(ctx, userKey, day, s.cfg.DailyLimit)
	if err != nil {
		log.Printf("Discordの利用回数を確保できません: %v", err)
		return "いま混み合っています。しばらくしてからお試しください。"
	}
	if !ok {
		return fmt.Sprintf("本日の質問回数（%d回）を使い切りました。日付が変わるとリセットされます。", s.cfg.DailyLimit)
	}

	var answer strings.Builder
	var sources []discord.Source
	err = s.pipe.RunWithMode(ctx, question, nil, nil, pipeline.ModeDeep, func(event pipeline.Event) {
		switch event.Type {
		case "delta":
			answer.WriteString(event.Text)
		case "pages":
			// **出典はサーバーが索引から組み立てる。** モデルが書けるのは番号だけ、
			// という性質をDiscordでも変えない（docs/02）
			sources = sources[:0]
			for _, page := range event.Pages {
				sources = append(sources, discord.Source{Title: page.Title, URL: page.URL})
			}
		}
	})
	if err != nil {
		if refundErr := s.state.Refund(ctx, userKey, day); refundErr != nil {
			log.Printf("Discordの利用回数の返却に失敗: %v", refundErr)
		}
		log.Printf("Discordの質問に失敗（%s）: %v", username, err)
		switch {
		case errors.Is(err, llm.ErrQuotaGuard), errors.Is(err, llm.ErrDailyQuota):
			return "本日のLLM利用上限に達しました。日付が変わってからお試しください。"
		case errors.Is(err, llm.ErrRateLimited):
			return "いま混み合っています。少し待ってからお試しください。"
		default:
			return "回答の生成に失敗しました。"
		}
	}
	return discord.FormatAnswer(question, answer.String(), sources)
}

// replyDiscord は「考えています」を実際の回答へ書き換える。
func (s *Server) replyDiscord(ctx context.Context, token, content string) {
	payload, err := json.Marshal(discord.NewFollowUp(content))
	if err != nil {
		log.Printf("Discordへの返信を組み立てられません: %v", err)
		return
	}
	url := discord.FollowUpURL(s.cfg.DiscordAppID, token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(payload))
	if err != nil {
		log.Printf("Discordへの返信を作れません: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Discordへ返信できません: %v", err)
		return
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		log.Printf("Discordが返信を受け付けません（%d）: %s", res.StatusCode, detail)
	}
}

func writeRaw(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
