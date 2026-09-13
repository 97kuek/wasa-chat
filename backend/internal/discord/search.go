package discord

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// SearchLimit はDiscordの検索が1回に返す上限。これ以上は offset で辿る。
const SearchLimit = 25

// SearchContextRadius はヒットの前後を何件ずつ取るか。
//
// **検索結果だけでは会話にならない。** 1行だけ見せられても、何の話の途中か
// 分からない。前後を足して初めて要約の材料になる（2026-09-13のCodex指摘）。
const SearchContextRadius = 5

// MaxSearchHits は前後を取りに行くヒットの数。
// ヒットごとに1リクエスト増えるので、ここで止める。
const MaxSearchHits = 5

// Search はサーバー内のメッセージを検索する。
//
// ⚠️ **ボットで検索できる。** 以前は「検索APIはユーザーアカウント専用」と
// 考えて全チャンネルを巡回する実装にしていたが、2026-03-19に公式が
// ボット向けに文書化していた（2026-09-13にCodexが指摘）。巡回は遅く、
// Discord側の負荷も大きい。
//
// 必要なのは対象チャンネルの READ_MESSAGE_HISTORY と MESSAGE_CONTENT intent。
//
// ⚠️ **読む先は allowed で渡されたチャンネルだけに絞る。** 検索APIは
// 「ボットが見える範囲」で認可されるので、そのまま使うと**ボットが入っている
// 非公開チャンネルまで画面から読めてしまう**。画面の利用者はWikiアカウントで、
// Discordの権限とは無関係である（docs/09 A-12）。
func Search(ctx context.Context, botToken, guildID, query string, allowed []Channel) ([]ChannelLog, error) {
	if botToken == "" {
		return nil, ErrNoBotToken
	}
	if guildID == "" {
		return nil, ErrNotInGuild
	}
	if len(allowed) == 0 {
		return nil, ErrNoPublicChannels
	}
	names := make(map[string]string, len(allowed))
	values := url.Values{}
	values.Set("content", strings.TrimSpace(query))
	for _, channel := range allowed {
		names[channel.ID] = channel.Name
		// **チャンネルを明示して検索する。** 省略すると、ボットが見える
		// 非公開チャンネルまで対象になる
		values.Add("channel_id", channel.ID)
	}
	values.Set("limit", fmt.Sprint(SearchLimit))
	values.Set("sort_by", "timestamp")
	values.Set("sort_order", "desc")

	f := &fetcher{token: botToken}
	var payload struct {
		// 検索結果は「ヒットとその前後」の組で返る
		Messages [][]Message `json:"messages"`
	}
	target := fmt.Sprintf("%s/guilds/%s/messages/search?%s", apiBase, guildID, values.Encode())
	if err := f.get(ctx, target, &payload); err != nil {
		return nil, err
	}

	hits := make([]Message, 0, len(payload.Messages))
	for _, group := range payload.Messages {
		for _, message := range group {
			// 組の中で hit が立っているものが本体。立っていなければ先頭を使う
			if message.Hit {
				hits = append(hits, message)
				break
			}
		}
		if len(hits) == 0 || !hits[len(hits)-1].Hit {
			if len(group) > 0 {
				hits = append(hits, group[0])
			}
		}
	}
	return withContext(ctx, f, hits, names)
}

// withContext はヒットの前後を取って、チャンネルごとの会話へ組み直す。
func withContext(ctx context.Context, f *fetcher, hits []Message, names map[string]string) ([]ChannelLog, error) {
	byChannel := map[string][]Message{}
	seen := map[string]bool{}
	for i, hit := range hits {
		if i >= MaxSearchHits {
			break
		}
		channelID := hit.ChannelID
		if _, ok := names[channelID]; !ok {
			continue // 許可していないチャンネルのヒットは捨てる
		}
		around, err := around(ctx, f, channelID, hit.ID)
		if err != nil {
			// 前後が取れなくてもヒット自体は使える
			around = []Message{hit}
		}
		for _, message := range around {
			if seen[message.ID] {
				continue // 近いヒット同士は前後が重なる
			}
			seen[message.ID] = true
			byChannel[channelID] = append(byChannel[channelID], message)
		}
	}

	logs := make([]ChannelLog, 0, len(byChannel))
	for channelID, messages := range byChannel {
		// Gather と同じく「新しい順」で持つ。Transcript が並べ直す
		sortNewestFirst(messages)
		logs = append(logs, ChannelLog{Channel: names[channelID], Messages: messages})
	}
	sortByChannelName(logs)
	return logs, nil
}

func around(ctx context.Context, f *fetcher, channelID, messageID string) ([]Message, error) {
	target := fmt.Sprintf("%s/channels/%s/messages?around=%s&limit=%d",
		apiBase, channelID, messageID, SearchContextRadius*2+1)
	var page []Message
	if err := f.get(ctx, target, &page); err != nil {
		return nil, err
	}
	return page, nil
}

func sortNewestFirst(messages []Message) {
	for i := 1; i < len(messages); i++ {
		for j := i; j > 0 && messages[j].Timestamp.After(messages[j-1].Timestamp); j-- {
			messages[j], messages[j-1] = messages[j-1], messages[j]
		}
	}
}

func sortByChannelName(logs []ChannelLog) {
	for i := 1; i < len(logs); i++ {
		for j := i; j > 0 && logs[j].Channel < logs[j-1].Channel; j-- {
			logs[j], logs[j-1] = logs[j-1], logs[j]
		}
	}
}

// SearchScope は画面へ出す「何を読んだか」の説明。
func SearchScope(query string, channels, messages int) string {
	return fmt.Sprintf("Discordの公開チャンネル%d件から「%s」を検索し、前後を含む%d件の発言を読みました",
		channels, strings.TrimSpace(query), messages)
}
