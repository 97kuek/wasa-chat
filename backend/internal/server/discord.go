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

// discordChoicesTimeout は補完の候補を作るまでの上限。
// Discordは3秒待ってくれるので、その手前で諦めて空の候補を返す。
const discordChoicesTimeout = 2 * time.Second

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
	case discord.TypeAutocomplete:
		// ⚠️ **補完は先延ばしできない。** ここで同期に返す
		writeRaw(w, s.discordChoices(r.Context(), &interaction))
	default:
		writeRaw(w, discord.Pong())
	}
}

// discordChoices は「範囲」を打っている最中の候補を返す。
//
// 3秒以内に返せないと候補が出ない。上流のDiscordへ問い合わせるのは
// 1回だけで、しかも1分間は覚えている（discord.ScopeChoices）。
func (s *Server) discordChoices(ctx context.Context, interaction *discord.Interaction) []byte {
	name, typed := interaction.Focused()
	ctx, cancel := context.WithTimeout(ctx, discordChoicesTimeout)
	defer cancel()

	switch name {
	case discord.OptionScope:
		if s.cfg.DiscordBotToken == "" {
			return discord.Autocomplete(nil)
		}
		return discord.Autocomplete(discord.ScopeChoices(ctx, s.cfg.DiscordBotToken, interaction.GuildID, typed))
	case discord.OptionAssistant:
		return discord.Autocomplete(s.assistantChoices(ctx, typed))
	default:
		return discord.Autocomplete(nil)
	}
}

// assistantChoices は `/wasa` のアシスタント候補。打つと絞られる。
func (s *Server) assistantChoices(ctx context.Context, typed string) []discord.Choice {
	assistants, err := s.state.ListAssistants(ctx)
	if err != nil {
		// 候補が出ないだけで、コマンド自体は打てる
		log.Printf("アシスタントの候補を作れません: %v", err)
		return nil
	}
	typed = strings.ToLower(strings.TrimSpace(typed))
	choices := make([]discord.Choice, 0, discord.AutocompleteLimit)
	for _, assistant := range assistants {
		if len(choices) >= discord.AutocompleteLimit {
			break
		}
		if typed != "" && !strings.Contains(strings.ToLower(assistant.Name), typed) {
			continue
		}
		choices = append(choices, discord.Choice{Name: assistant.Name, Value: assistant.ID})
	}
	return choices
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
	channelID := interaction.ChannelID
	assistantID := interaction.OptionText(discord.OptionAssistant)
	target := recapTarget{
		guildID:   interaction.GuildID,
		channelID: interaction.ChannelID,
		scope:     interaction.OptionText(discord.OptionScope),
		options: discord.Options{
			Days: interaction.OptionInt(discord.OptionDays, discord.DefaultDays),
		},
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), discordAnswerTimeout)
		defer cancel()

		var reply discordReply
		switch command {
		case discord.CommandSummary:
			reply = s.recapForDiscord(ctx, recap.KindSummary, target, userID, username)
		case discord.CommandTodo:
			reply = s.recapForDiscord(ctx, recap.KindTodo, target, userID, username)
		default:
			reply = s.answerForDiscord(ctx, question, assistantID, channelID, userID, username)
		}
		if reply.refusal != "" {
			s.refuseDiscord(ctx, token, reply.refusal)
			return
		}
		s.replyDiscord(ctx, token, reply.messages)
	}()
}

// discordReply は Discord へ返すもの。
//
// refusal は**本人だけに見える知らせ**（回数切れ・操作ミス・失敗）。
// チャンネル全員に見せる必要が無いものを分けている。
type discordReply struct {
	messages []string
	refusal  string
}

func refuse(format string, args ...any) discordReply {
	return discordReply{refusal: fmt.Sprintf(format, args...)}
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

使える記法（Discordが実際に描画します）
- 見出しは ### だけを使う。# と ## はDiscordでは大きすぎる
- **太字** で語を強調する。__下線__ も使える
- 箇条書きは行頭に - 。入れ子は半角スペース2つで字下げする
- 順番があるものは 1. 2. 3.
- リンクは [文字](URL) の形にする

使えない記法（そのまま文字として出てしまいます）
- 表は使わない。項目が複数あるときは箇条書きにする
- 図（mermaid）は使わない
- - [ ] のチェックボックスは使わない

その他
- 全体を1200文字以内に収める。長い前置きは書かない
- 節が2つ以上に分かれるときだけ ### を使う。短い回答に見出しは付けない`,
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
//
// ⚠️ **指示語を必須にしている。** 以前は「会話」を含むだけで拾っていたため、
// 「過去の会話ログはWikiにある？」のような**普通の質問まで案内に化けた**
// （2026-09-13に指摘）。「この会話」「ここまでのやりとり」のように、
// いま目の前のやりとりを指していると読める形だけを対象にする。
var recapRequestPattern = regexp.MustCompile(`(この|ここまでの?|今までの|直近の)(会話|やりとり|スレッド|話)`)

// recapVerbs は「何をしてほしいか」。指示語と両方そろったときだけ案内する。
var recapVerbs = map[string][]string{
	"`/要約`":   {"要約", "まとめて", "まとめを"},
	"`/todo`": {"ToDo", "todo", "TODO", "タスク", "やること"},
}

// recapRedirect は、会話そのものを扱う依頼なら案内先のコマンドを返す。
//
// **指示語と依頼の両方がそろったときだけ**案内する。片方だけで拾うと、
// 「議事録のまとめ方は？」のような普通の質問まで案内に化ける。
func recapRedirect(question string) string {
	if !recapRequestPattern.MatchString(question) {
		return ""
	}
	// map を回すと順序が毎回変わる。並びを固定して、同じ質問には同じ案内を返す
	for _, command := range []string{"`/要約`", "`/todo`"} {
		for _, verb := range recapVerbs[command] {
			if strings.Contains(question, verb) {
				return command
			}
		}
	}
	return ""
}

func (s *Server) answerForDiscord(ctx context.Context, question, assistantID, channelID, userID, username string) discordReply {
	if question == "" {
		return refuse("質問を入力してください。")
	}
	if len([]rune(question)) > maxQuestionRunes {
		return refuse("質問は%d文字以内にしてください。", maxQuestionRunes)
	}
	if command := recapRedirect(question); command != "" {
		return refuse("会話そのものを扱うときは %s を使ってください。`/wasa` は引き継ぎ資料への質問用です。", command)
	}

	assistant, err := s.discordAssistant(ctx, assistantID)
	if err != nil {
		return refuse("そのアシスタントは見つかりませんでした。")
	}

	refund, message := s.takeDiscordQuota(ctx, userID)
	if refund == nil {
		return refuse("%s", message)
	}

	// **続けて聞けるようにする。** 同じチャンネルで同じ人が直前にした
	// やりとりだけを渡す。画面の履歴とは別枠（docs/07 §5.5）
	history := s.discordHistory(ctx, userID, channelID)

	var answer strings.Builder
	var sources []discord.Source
	err = s.pipe.RunWithMode(ctx, question, history, assistant, pipeline.ModeDeep, func(event pipeline.Event) {
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
		return refuse("%s", discordLLMError(err))
	}
	s.saveDiscordHistory(ctx, userID, channelID, question, answer.String())
	return discordReply{messages: discord.FormatAnswer(question, answer.String(), sources)}
}

// recapTarget はどこを読むか。
type recapTarget struct {
	guildID   string
	channelID string
	// scope は「範囲」オプションの値。空ならこのチャンネル、
	// discord.ScopeAllChannels なら横断、それ以外はチャンネルID
	scope string
	// label は読んだ範囲の呼び名。回答の先頭にそのまま出る
	label   string
	options discord.Options
}

// resolve は「範囲」を、実際に読む対象へ落とす。
//
// ⚠️ **候補に出したものだけが選ばれるとは限らない。** 「範囲」は文字列の
// オプションなので、利用者は候補に無いチャンネルIDを手で打てる。
// **打ったチャンネル以外を読むときは、公開チャンネルであることを必ず確かめる**
// （ボットが見えるチャンネルと、打った人が見えるチャンネルは違う）。
func (t recapTarget) resolve(ctx context.Context, botToken string) (recapTarget, string) {
	switch {
	case t.scope == "" || t.scope == t.channelID:
		t.label = "このチャンネル"
		return t, ""
	case t.scope == discord.ScopeAllChannels:
		t.options.AllChannels = true
		return t, "" // 読めたチャンネル数が決まってから名前を付ける
	}
	channel, ok := discord.PublicChannel(ctx, botToken, t.guildID, t.scope)
	if !ok {
		return t, "そのチャンネルは読めません。範囲は候補から選んでください（非公開チャンネルは読みません）。"
	}
	t.channelID, t.label = channel.ID, "#"+channel.Name
	return t, ""
}

// recapForDiscord はチャンネルの会話を要約する／ToDoを抜き出す。
//
// **索引を読まない。** 根拠は会話ログそのもので、出典も付かない。
// なぜ回答パイプラインに載せないかは internal/recap のパッケージ説明を参照。
func (s *Server) recapForDiscord(ctx context.Context, kind recap.Kind, target recapTarget, userID, username string) discordReply {
	if s.recap == nil || s.cfg.DiscordBotToken == "" {
		return refuse("この機能はまだ設定されていません（DISCORD_BOT_TOKENが未設定）。")
	}
	if target.channelID == "" {
		return refuse("チャンネルの中で実行してください。")
	}
	target, refusal := target.resolve(ctx, s.cfg.DiscordBotToken)
	if refusal != "" {
		return refuse("%s", refusal)
	}

	// **先に会話ログを取る。** 読めないチャンネルだったときに枠を減らさない
	logs, err := discord.Gather(ctx, s.cfg.DiscordBotToken, target.guildID, target.channelID, target.options)
	if err != nil {
		switch {
		case errors.Is(err, discord.ErrNoAccess):
			return refuse("このチャンネルの過去ログを読む権限がありません。WASA Chatに「メッセージ履歴を読む」権限を与えてください。")
		case errors.Is(err, discord.ErrNotInGuild):
			return refuse("チャンネル横断の要約は、サーバーの中で実行してください。")
		case errors.Is(err, discord.ErrNoPublicChannels):
			return refuse("読める公開チャンネルがありませんでした。WASA Chatに「メッセージ履歴を読む」権限を与えてください。")
		}
		log.Printf("Discordの過去ログ取得に失敗（%s）: %v", username, err)
		return refuse("過去ログを読み取れませんでした。")
	}
	transcript := discord.Transcript(logs)
	if strings.TrimSpace(transcript) == "" {
		return refuse("読み取れる発言がありませんでした。期間を広げてお試しください。")
	}

	refund, message := s.takeDiscordQuota(ctx, userID)
	if refund == nil {
		return refuse("%s", message)
	}
	body, err := s.recap.Run(ctx, kind, transcript)
	if err != nil {
		refund()
		log.Printf("Discordの%s生成に失敗（%s）: %v", kind, username, err)
		return refuse("%s", discordLLMError(err))
	}
	where := target.label
	if target.options.AllChannels {
		where = fmt.Sprintf("公開チャンネル%d件", len(logs))
	}
	scope := discord.RecapScope(where, target.options.Days,
		discord.CountMessages(logs), discord.CountSpeakers(logs))
	return discordReply{messages: discord.FormatRecap(scope, body)}
}

// replyDiscord は「考えています」を実際の回答へ書き換える。
//
// **回答は複数通になり得る。** 1通目は最初の応答を書き換え、続きは追加の
// メッセージとして投稿する。出典URLは日本語タイトルのパーセントエンコードで
// 最長214文字あり、切り詰めると長い回答が毎回途中で切れていた（2026-09-13）。
func (s *Server) replyDiscord(ctx context.Context, token string, messages []string) {
	if len(messages) == 0 {
		return
	}
	url := discord.FollowUpURL(s.cfg.DiscordAppID, token)
	if err := s.sendDiscord(ctx, http.MethodPatch, url, discord.NewFollowUp(messages[0])); err != nil {
		log.Printf("Discordへ返信できません: %v", err)
		return
	}
	extra := discord.ExtraMessageURL(s.cfg.DiscordAppID, token)
	for _, message := range messages[1:] {
		if err := s.sendDiscord(ctx, http.MethodPost, extra, discord.NewFollowUp(message)); err != nil {
			// 続きが出せなくても、1通目は既に出ている。そこで止める
			log.Printf("Discordへ続きを出せません: %v", err)
			return
		}
	}
}

// refuseDiscord は失敗や操作ミスを**本人だけに見える形**で伝える。
//
// 「本日の回数を使い切りました」をチャンネル全員に見せる必要は無い。
// 「考えています」を消してから、本人向けの知らせを出す。
func (s *Server) refuseDiscord(ctx context.Context, token, reason string) {
	url := discord.DeleteOriginalURL(s.cfg.DiscordAppID, token)
	if err := s.sendDiscord(ctx, http.MethodDelete, url, nil); err != nil {
		// 消せなければ、そのまま書き換えて伝える（黙って終わらせない）
		log.Printf("Discordの「考えています」を消せません: %v", err)
		s.replyDiscord(ctx, token, []string{reason})
		return
	}
	extra := discord.ExtraMessageURL(s.cfg.DiscordAppID, token)
	if err := s.sendDiscord(ctx, http.MethodPost, extra, discord.NewEphemeral(reason)); err != nil {
		log.Printf("Discordへ知らせを出せません: %v", err)
	}
}

func (s *Server) sendDiscord(ctx context.Context, method, url string, payload any) error {
	var reader io.Reader
	if payload != nil {
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("Discordが受け付けません（%d）: %s", res.StatusCode, detail)
	}
	return nil
}

func writeRaw(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// discordAssistant は `/wasa` の「アシスタント」オプションを解決する。
//
// 指定が無ければ discordStyle（Discord向けの書き方の指定だけを持つもの）。
// 指定があれば、その口調・参照範囲の上に**Discord向けの書き方を重ねる**。
// 画面と同じアシスタントをそのまま使うと、表やMermaidを書いてきて崩れる。
func (s *Server) discordAssistant(ctx context.Context, id string) (*state.Assistant, error) {
	if id == "" {
		return discordStyle, nil
	}
	assistants, err := s.state.ListAssistants(ctx)
	if err != nil {
		return nil, err
	}
	for _, assistant := range assistants {
		if assistant.ID != id {
			continue
		}
		merged := assistant
		merged.Instruction = strings.TrimSpace(assistant.Instruction) + "\n\n" + discordStyle.Instruction
		return &merged, nil
	}
	return nil, errAssistantNotFound
}

var errAssistantNotFound = errors.New("アシスタントが見つかりません")

// discordChatID は会話の続きを入れておく場所。
//
// **チャンネルごと・利用者ごと**に分ける。同じチャンネルでも別の人の質問が
// 混ざると文脈がおかしくなるし、同じ人でも別チャンネルなら別の話をしている。
func discordChatID(channelID string) string { return "discord:" + channelID }

// discordHistoryTurns は回答へ渡す直前のやりとりの数。
//
// **少なくてよい。** 履歴は毎回プロンプトへ載るので、多いほど資料に使える
// 文脈が減る。画面と違ってDiscordには履歴の画面が無く、続けて聞くのは
// たいてい直前の1〜2往復である。
const discordHistoryTurns = 3

// discordHistory は同じチャンネル・同じ利用者の直前のやりとりを返す。
func (s *Server) discordHistory(ctx context.Context, userID, channelID string) []pipeline.ConversationTurn {
	chats, err := s.state.ListChats(ctx, s.userKey("discord:"+userID), maxChats)
	if err != nil {
		// 続きが効かないだけで、質問そのものは答えられる
		log.Printf("Discordの履歴を読めません: %v", err)
		return nil
	}
	id := discordChatID(channelID)
	for _, chat := range chats {
		if chat.ID != id {
			continue
		}
		turns := chat.Turns
		if len(turns) > discordHistoryTurns {
			turns = turns[len(turns)-discordHistoryTurns:]
		}
		history := make([]pipeline.ConversationTurn, 0, len(turns))
		for _, turn := range turns {
			if turn.Question == "" || turn.Answer == "" {
				continue
			}
			history = append(history, pipeline.ConversationTurn{Question: turn.Question, Answer: turn.Answer})
		}
		return history
	}
	return nil
}

// saveDiscordHistory は次の質問へ渡すために、今回のやりとりを覚えておく。
//
// **画面の履歴とは別枠にする。** 同じ人でもDiscordと画面では文脈が違い、
// 混ぜると画面のチャット一覧にDiscordのやりとりが並んでしまう。
func (s *Server) saveDiscordHistory(ctx context.Context, userID, channelID, question, answer string) {
	if channelID == "" || strings.TrimSpace(answer) == "" {
		return
	}
	userKey := s.userKey("discord:" + userID)
	id := discordChatID(channelID)
	now := time.Now().UTC().Format(time.RFC3339)

	chat := state.Chat{ID: id, Title: "Discord", CreatedAt: now}
	if chats, err := s.state.ListChats(ctx, userKey, maxChats); err == nil {
		for _, found := range chats {
			if found.ID == id {
				chat = found
				break
			}
		}
	}
	chat.UpdatedAt = now
	chat.Turns = append(chat.Turns, state.Turn{Question: question, Answer: answer, Status: "done"})
	// **際限なく伸ばさない。** 履歴はFirestoreの1ドキュメント1MBに収める必要がある
	if len(chat.Turns) > discordHistoryTurns {
		chat.Turns = chat.Turns[len(chat.Turns)-discordHistoryTurns:]
	}
	if err := s.state.SaveChat(ctx, userKey, state.NormalizeChat(chat), maxChats); err != nil {
		log.Printf("Discordの履歴を保存できません: %v", err)
	}
}
