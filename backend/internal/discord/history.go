package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// pageSize はDiscordが1回に返せる上限。これ以上は `before` を渡して遡る。
const pageSize = 100

// MaxMessages は1回のコマンドで集める発言の総数。
//
// **上限は要る。** 何年ぶんでも遡れてしまうと、1回のコマンドで数百リクエストを
// 投げ、上流へ数十万字を送ることになる。日数の指定（Days）を1年まで広げたぶん
// （2026-09-13）、実質の歯止めはこちらになった。
//
// 5000件 × 1件あたり50字程度 = 25万字。recap 側は6万字ごとに分割するので、
// 分割の上限（6塊）に収まる。
const MaxMessages = 5000

// MaxRequests は1回のコマンドで投げるDiscordへのリクエスト数の上限。
// チャンネル横断だと、チャンネル数 × ページ数だけ増える。
// 120回 × 250ms = 30秒。Discordのトークンは15分もつので間に合う。
const MaxRequests = 120

// requestInterval はDiscordへの連投を避ける間隔。
// 1ルート5回/5秒あたりで429が返るため、余裕をもって空ける。
// テストから0にできるよう var にしてある（本番で書き換えないこと）。
var requestInterval = 250 * time.Millisecond

// apiBase は Discord API の宛先。テストから差し替えるために var にしてある。
const defaultAPIBase = "https://discord.com/api/v10"

var apiBase = defaultAPIBase

// 遡れる日数。既定は7日、上限は1年。
//
// 90日から広げた（2026-09-13）。代がまたがる長さを読めるようにするため。
// 実際に読む量は MaxMessages と MaxRequests が抑える。
const (
	DefaultDays = 7
	MaxDays     = 365
)

// MessageLengthLimit は1発言あたりの上限。1人の長文でログが埋まるのを防ぐ。
const MessageLengthLimit = 600

// permViewChannel は「チャンネルを見る」権限のビット。公開判定に使う。
const permViewChannel = 1 << 10

// チャンネルの種類。スレッドや音声は読まない。
const (
	channelTypeText         = 0
	channelTypeAnnouncement = 5
)

// Message は過去ログの1件。必要な項目だけ拾う。
type Message struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Author  struct {
		ID string `json:"id"`
		// GlobalName は表示名。未設定の利用者がいるので Username へ落とす
		GlobalName string `json:"global_name"`
		Username   string `json:"username"`
		Bot        bool   `json:"bot"`
	} `json:"author"`
	Timestamp time.Time `json:"timestamp"`
}

func (m *Message) name() string {
	if m.Author.GlobalName != "" {
		return m.Author.GlobalName
	}
	return m.Author.Username
}

// Channel はギルドのチャンネル。
type Channel struct {
	ID                 string `json:"id"`
	Type               int    `json:"type"`
	Name               string `json:"name"`
	Position           int    `json:"position"`
	PermissionOverwrit []struct {
		ID   string `json:"id"`
		Type int    `json:"type"`
		Deny string `json:"deny"`
	} `json:"permission_overwrites"`
}

// public は @everyone から見えるチャンネルかを返す。
//
// ⚠️ **ここがチャンネル横断の安全弁である。** ボットが見えるチャンネルと、
// コマンドを打った人が見えるチャンネルは**同じではない**。ボットが幹部用の
// 非公開チャンネルに入っていると、1年生が横断要約を打っただけで、本来読めない
// 内容が要約として流れてしまう。
//
// 「@everyone が見られるチャンネルだけ」に限れば、**コマンドを打った人が
// 自分で開けば読めたもの**しか混ざらない（docs/09 A-9）。
func (c Channel) public(guildID string) bool {
	if c.Type != channelTypeText && c.Type != channelTypeAnnouncement {
		return false
	}
	for _, overwrite := range c.PermissionOverwrit {
		// @everyone のロールIDはギルドIDと同じ
		if overwrite.ID != guildID {
			continue
		}
		deny, err := strconv.ParseUint(overwrite.Deny, 10, 64)
		if err != nil {
			// 読めない値は**安全側**に倒す。見えない扱いにする
			return false
		}
		if deny&permViewChannel != 0 {
			return false
		}
	}
	return true
}

// ChannelLog は1チャンネルぶんの発言。
type ChannelLog struct {
	Channel  string
	Messages []Message
}

// Options は集める範囲。
type Options struct {
	// Days は何日前まで遡るか。0以下なら DefaultDays
	Days int
	// AllChannels が true なら、サーバーの**公開**チャンネルを横断する
	AllChannels bool
}

func (o Options) days() int {
	switch {
	case o.Days <= 0:
		return DefaultDays
	case o.Days > MaxDays:
		return MaxDays
	default:
		return o.Days
	}
}

// fetcher はリクエスト数と間隔を1回のコマンド全体で管理する。
// チャンネルごとに作り直すと、上限が「チャンネルあたり」になってしまう。
type fetcher struct {
	token    string
	requests int
	last     time.Time
}

func (f *fetcher) get(ctx context.Context, url string, out any) error {
	if f.requests >= MaxRequests {
		return errTooManyRequests
	}
	if wait := requestInterval - time.Since(f.last); wait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+f.token)
	f.requests++
	f.last = time.Now()

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusNotFound {
		return ErrNoAccess
	}
	if res.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 256))
		return fmt.Errorf("Discordが応答しません（%d）: %s", res.StatusCode, detail)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// Gather は要約の対象になる発言を集める。
//
// 1チャンネルぶんでも `before` で遡ってページングする。Discordは1回に100件しか
// 返さないので、**「直近100件」で固定すると、話が続いた日の午後には
// 午前の話が読めなくなる**（2026-09-13の指摘）。
//
// AllChannels が立っていれば、サーバーの公開チャンネルを横断する。
// 非公開チャンネルは読まない（Channel.public の説明を参照）。
func Gather(ctx context.Context, botToken, guildID, channelID string, opts Options) ([]ChannelLog, error) {
	if botToken == "" {
		return nil, ErrNoBotToken
	}
	since := time.Now().AddDate(0, 0, -opts.days())
	f := &fetcher{token: botToken}

	targets := []Channel{{ID: channelID}}
	if opts.AllChannels {
		if guildID == "" {
			return nil, ErrNotInGuild
		}
		found, err := publicChannels(ctx, f, guildID)
		if err != nil {
			return nil, err
		}
		targets = found
	}

	// 横断のときは**発言数の枠をチャンネルで分け合う**。1つのにぎやかな
	// チャンネルが枠を食い潰して、他が1件も読まれない、という偏りを避ける
	perChannel := MaxMessages
	if len(targets) > 1 {
		perChannel = MaxMessages / len(targets)
		if perChannel < pageSize {
			perChannel = pageSize
		}
	}

	logs := make([]ChannelLog, 0, len(targets))
	total := 0
	for _, target := range targets {
		if total >= MaxMessages {
			break
		}
		budget := min(perChannel, MaxMessages-total)
		messages, err := fetchChannel(ctx, f, target.ID, since, budget)
		if err != nil {
			// **1チャンネル指定のときは、失敗をそのまま伝える。** ここを
			// 飛ばすと「読めなかった」が「発言が無かった」に化ける
			if !opts.AllChannels {
				return nil, err
			}
			// 横断のときは飛ばす。1つの権限不足で全部が失敗しては使い物にならない
			continue
		}
		if len(messages) == 0 {
			continue
		}
		total += len(messages)
		logs = append(logs, ChannelLog{Channel: target.Name, Messages: messages})
	}
	return logs, nil
}

func publicChannels(ctx context.Context, f *fetcher, guildID string) ([]Channel, error) {
	var channels []Channel
	url := fmt.Sprintf("%s/guilds/%s/channels", apiBase, guildID)
	if err := f.get(ctx, url, &channels); err != nil {
		return nil, err
	}
	open := channels[:0]
	for _, channel := range channels {
		if channel.public(guildID) {
			open = append(open, channel)
		}
	}
	// 画面に並ぶ順で読む。要約の見出しの順が毎回変わらないようにする
	sort.SliceStable(open, func(a, b int) bool { return open[a].Position < open[b].Position })
	if len(open) == 0 {
		return nil, ErrNoPublicChannels
	}
	return open, nil
}

// fetchChannel は1チャンネルを `before` で遡って読む。新しい順で返す。
func fetchChannel(ctx context.Context, f *fetcher, channelID string, since time.Time, budget int) ([]Message, error) {
	var collected []Message
	before := ""
	for len(collected) < budget {
		url := fmt.Sprintf("%s/channels/%s/messages?limit=%d", apiBase, channelID, pageSize)
		if before != "" {
			url += "&before=" + before
		}
		var page []Message
		if err := f.get(ctx, url, &page); err != nil {
			// 途中まで読めていれば、それを使う。全部捨てるほうが損
			if len(collected) > 0 && (err == errTooManyRequests || err == ErrNoAccess) {
				return collected, nil
			}
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		reachedEnd := false
		for _, message := range page {
			if message.Timestamp.Before(since) {
				reachedEnd = true
				break
			}
			collected = append(collected, message)
			if len(collected) >= budget {
				break
			}
		}
		if reachedEnd || len(page) < pageSize {
			break
		}
		before = page[len(page)-1].ID
	}
	return collected, nil
}

// CountMessages は集めた発言のうち、実際に要約へ渡る件数を数える。
func CountMessages(logs []ChannelLog) int {
	total := 0
	for _, log := range logs {
		for _, message := range log.Messages {
			if !message.Author.Bot && strings.TrimSpace(message.Content) != "" {
				total++
			}
		}
	}
	return total
}

// CountSpeakers は会話ログに何人が出てくるかを数える。読んだ範囲の説明に使う。
func CountSpeakers(logs []ChannelLog) int {
	seen := map[string]struct{}{}
	for _, log := range logs {
		for _, message := range log.Messages {
			if message.Author.Bot || strings.TrimSpace(message.Content) == "" {
				continue
			}
			seen[message.Author.ID] = struct{}{}
		}
	}
	return len(seen)
}

// Transcript は集めた発言を、上流へ渡せる1本の文字列にする。
//
// Discord は**新しい順**で返すので、読める向き（古い順）へ並べ直す。
//
// ボットの発言は落とす。WASA自身の回答まで混ぜると、**自分の要約を要約する**
// ことになって内容が薄まる。本文が空の発言（添付や埋め込みだけ）も落とす。
//
// **切り詰めはここでしない。** 長いときに古いほうを捨てると、前の代で出た案が
// 黙って消える。全部渡して、分割して読むかどうかは recap 側が決める
// （2026-09-13の指摘）。
func Transcript(logs []ChannelLog) string {
	var out strings.Builder
	for _, log := range logs {
		lines := make([]string, 0, len(log.Messages))
		// 新しい順で受け取るので、後ろから詰めて古い順にする
		for i := len(log.Messages) - 1; i >= 0; i-- {
			if line := formatMessage(log.Messages[i]); line != "" {
				lines = append(lines, line)
			}
		}
		if len(lines) == 0 {
			continue
		}
		if out.Len() > 0 {
			out.WriteString("\n\n")
		}
		// チャンネル名は横断のときだけ意味がある。1チャンネルなら付けない
		if log.Channel != "" {
			fmt.Fprintf(&out, "## #%s\n", log.Channel)
		}
		out.WriteString(strings.Join(lines, "\n"))
	}
	return out.String()
}

func formatMessage(message Message) string {
	if message.Author.Bot {
		return ""
	}
	content := strings.TrimSpace(message.Content)
	if content == "" {
		return ""
	}
	if runes := []rune(content); len(runes) > MessageLengthLimit {
		content = string(runes[:MessageLengthLimit]) + "…"
	}
	// 改行を含む発言でも1行1発言に見えるようにする
	content = strings.ReplaceAll(content, "\n", " / ")
	return fmt.Sprintf("%s %s: %s",
		message.Timestamp.In(japanTime).Format("01/02"), message.name(), content)
}

var japanTime = time.FixedZone("JST", 9*60*60)

var (
	// ErrNoBotToken は DISCORD_BOT_TOKEN が未設定であることを表す。
	ErrNoBotToken = fmt.Errorf("BOTトークンが設定されていません")
	// ErrNoAccess はそのチャンネルの過去ログを読む権限が無いことを表す。
	ErrNoAccess = fmt.Errorf("このチャンネルの過去ログを読めません")
	// ErrNotInGuild はDMで横断要約を求められたことを表す。
	ErrNotInGuild = fmt.Errorf("サーバーの中で実行してください")
	// ErrNoPublicChannels は読める公開チャンネルが1つも無いことを表す。
	ErrNoPublicChannels = fmt.Errorf("読める公開チャンネルがありません")

	// errTooManyRequests は1回のコマンドのリクエスト上限に達したことを表す。
	// 利用者へは出さない（読めたところまでで答える）。
	errTooManyRequests = fmt.Errorf("リクエスト数の上限に達しました")
)
