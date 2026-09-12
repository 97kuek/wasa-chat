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
	"regexp"
	"strings"
	"time"

	"github.com/97kuek/wasa-chat/backend/internal/discord"
	"github.com/97kuek/wasa-chat/backend/internal/llm"
	"github.com/97kuek/wasa-chat/backend/internal/pipeline"
	"github.com/97kuek/wasa-chat/backend/internal/recap"
	"github.com/97kuek/wasa-chat/backend/internal/state"
)

// Discordからの対話を受ける。
//
// ⚠️ **ここは認証ミドルウェアを通さない。** Cookieではなく、Discordの署名で
// 本人確認する。代わりに署名の検証を外さないこと（外すと誰でも叩けて、
// 無料枠を好きなだけ消費させられる）。
//
// ⚠️ **誰が使えるかは Discord のサーバー参加者で決まる。** Wikiアカウントでは
// ないので、退部者を止めるには Discord 側から外す必要がある。つまり
// **失効の手段が2か所になる**（docs/09 A-7）。2026-09-13にPMが承認した運用。
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
	// goroutine へ渡す前に値を写す。interaction はこの後で使い回さない
	command := interaction.CommandName()
	question := strings.TrimSpace(interaction.Question())
	userID := interaction.UserID()
	username := interaction.Username()
	token := interaction.Token
	target := recapTarget{
		guildID:   interaction.GuildID,
		channelID: interaction.ChannelID,
		options: discord.Options{
			Days:        interaction.OptionInt(discord.OptionDays, discord.DefaultDays),
			AllChannels: interaction.OptionText(discord.OptionScope) == discord.ScopeAllChannels,
		},
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), discordAnswerTimeout)
		defer cancel()

		var content string
		switch command {
		case discord.CommandSummary:
			content = s.recapForDiscord(ctx, recap.KindSummary, target, userID, username)
		case discord.CommandTodo:
			content = s.recapForDiscord(ctx, recap.KindTodo, target, userID, username)
		default:
			content = s.answerForDiscord(ctx, question, userID, username)
		}
		s.replyDiscord(ctx, token, content)
	}()
}

// discordStyle は Discord へ出すときの書き方の指定。
//
// **Discord は表もMermaidの図もレンダリングしない。** 画面向けの回答をそのまま
// 流すと、`| 項目 | 値 |` が等幅でないフォントで崩れ、図はただの文字列になる。
// 1メッセージ2000字という上限もある。
//
// アシスタントの仕組みに乗せているのは、**口調・書き方の指定を入れる場所が
// もともとそこだから**である。参照範囲は指定しない（全範囲で答える）。
var discordStyle = &state.Assistant{
	ID:   "discord",
	Name: "WASA Chat",
	Instruction: `Discordのメッセージとして読まれます。次の形式で書いてください。

- 全体を1200文字以内に収める。長い前置きは書かない
- 表は使わない。項目が複数あるときは箇条書きにする
- 図（mermaid）は使わない。Discordでは図として表示されず、文字列のまま出る
- 見出しは使わず、箇条書きと短い段落で構成する`,
}

// takeDiscordQuota は利用回数を1回ぶん確保する。
//
// 利用回数はDiscordの利用者ごとに数える。**Wikiの利用者とは別枠になる。**
// 同じ人が画面とDiscordで別々に数えられるが、無料枠そのものは
// 送信前の判定（GEMINI_RPD_LIMIT）が守る。
//
// 確保できたら後始末（返却）の関数を返す。確保できなければ nil と、
// 利用者へそのまま返す文言を返す。
func (s *Server) takeDiscordQuota(ctx context.Context, userID string) (refund func(), message string) {
	day := time.Now().In(japanTime).Format("2006-01-02")
	userKey := s.userKey("discord:" + userID)
	ok, err := s.state.Take(ctx, userKey, day, s.cfg.DailyLimit)
	if err != nil {
		log.Printf("Discordの利用回数を確保できません: %v", err)
		return nil, "いま混み合っています。しばらくしてからお試しください。"
	}
	if !ok {
		return nil, fmt.Sprintf("本日の利用回数（%d回）を使い切りました。日付が変わるとリセットされます。", s.cfg.DailyLimit)
	}
	return func() {
		if err := s.state.Refund(ctx, userKey, day); err != nil {
			log.Printf("Discordの利用回数の返却に失敗: %v", err)
		}
	}, ""
}

// discordLLMError は上流の失敗を利用者向けの文言へ落とす。
func discordLLMError(err error) string {
	switch {
	case errors.Is(err, llm.ErrQuotaGuard), errors.Is(err, llm.ErrDailyQuota):
		return "本日のLLM利用上限に達しました。日付が変わってからお試しください。"
	case errors.Is(err, llm.ErrRateLimited):
		return "いま混み合っています。少し待ってからお試しください。"
	default:
		return "生成に失敗しました。"
	}
}

// recapRequestPattern は `/wasa この会話を要約して` のような打ち間違いを拾う。
//
// 質問としてパイプラインへ流すと、会話の内容は索引に無いので「記載なし」と
// 返ってしまう。**枠を使う前に気づかせる**（送信しないので回数も減らさない）。
var recapRequestPattern = regexp.MustCompile(`(この|ここまでの|今までの)?(会話|やりとり|スレ)`)

func (s *Server) answerForDiscord(ctx context.Context, question, userID, username string) string {
	if question == "" {
		return "質問を入力してください。"
	}
	if len([]rune(question)) > maxQuestionRunes {
		return fmt.Sprintf("質問は%d文字以内にしてください。", maxQuestionRunes)
	}
	if recapRequestPattern.MatchString(question) {
		if strings.Contains(question, "要約") || strings.Contains(question, "まとめ") {
			return "会話の要約は `/要約` を使ってください。`/wasa` は引き継ぎ資料への質問用です。"
		}
		if strings.Contains(question, "ToDo") || strings.Contains(question, "todo") ||
			strings.Contains(question, "タスク") || strings.Contains(question, "やること") {
			return "会話からのToDo抽出は `/todo` を使ってください。`/wasa` は引き継ぎ資料への質問用です。"
		}
	}

	refund, message := s.takeDiscordQuota(ctx, userID)
	if refund == nil {
		return message
	}

	var answer strings.Builder
	var sources []discord.Source
	err := s.pipe.RunWithMode(ctx, question, nil, discordStyle, pipeline.ModeDeep, func(event pipeline.Event) {
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
		refund()
		log.Printf("Discordの質問に失敗（%s）: %v", username, err)
		return discordLLMError(err)
	}
	return discord.FormatAnswer(question, answer.String(), sources)
}

// recapTarget はどこを読むか。
type recapTarget struct {
	guildID   string
	channelID string
	options   discord.Options
}

// recapForDiscord はチャンネルの会話を要約する／ToDoを抜き出す。
//
// **索引を読まない。** 根拠は会話ログそのもので、出典も付かない。
// なぜ回答パイプラインに載せないかは internal/recap のパッケージ説明を参照。
func (s *Server) recapForDiscord(ctx context.Context, kind recap.Kind, target recapTarget, userID, username string) string {
	if s.recap == nil || s.cfg.DiscordBotToken == "" {
		return "この機能はまだ設定されていません（DISCORD_BOT_TOKENが未設定）。"
	}
	if target.channelID == "" {
		return "チャンネルの中で実行してください。"
	}

	// **先に会話ログを取る。** 読めないチャンネルだったときに枠を減らさない
	logs, err := discord.Gather(ctx, s.cfg.DiscordBotToken, target.guildID, target.channelID, target.options)
	if err != nil {
		switch {
		case errors.Is(err, discord.ErrNoAccess):
			return "このチャンネルの過去ログを読む権限がありません。WASA Chatに「メッセージ履歴を読む」権限を与えてください。"
		case errors.Is(err, discord.ErrNotInGuild):
			return "チャンネル横断の要約は、サーバーの中で実行してください。"
		case errors.Is(err, discord.ErrNoPublicChannels):
			return "読める公開チャンネルがありませんでした。WASA Chatに「メッセージ履歴を読む」権限を与えてください。"
		}
		log.Printf("Discordの過去ログ取得に失敗（%s）: %v", username, err)
		return "過去ログを読み取れませんでした。"
	}
	transcript := discord.Transcript(logs)
	if strings.TrimSpace(transcript) == "" {
		return "読み取れる発言がありませんでした。期間を広げてお試しください。"
	}

	refund, message := s.takeDiscordQuota(ctx, userID)
	if refund == nil {
		return message
	}
	body, err := s.recap.Run(ctx, kind, transcript)
	if err != nil {
		refund()
		log.Printf("Discordの%s生成に失敗（%s）: %v", kind, username, err)
		return discordLLMError(err)
	}
	scope := discord.RecapScope(
		target.options.Days, discord.CountMessages(logs), discord.CountSpeakers(logs), len(logs))
	return discord.FormatRecap(scope, body)
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
