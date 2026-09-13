package discord

import (
	"context"
	"fmt"
	"net/url"
	"sort"
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
//
// 5件だと「Discordを見ている感じがしない」という指摘が出たため12件へ広げた
// （2026-09-13）。前後5件ずつ付くので、最大で12×11＝132件の発言が材料になる。
const MaxSearchHits = 12

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
func Search(ctx context.Context, botToken, guildID string, terms []string, allowed []Channel) ([]ChannelLog, error) {
	if botToken == "" {
		return nil, ErrNoBotToken
	}
	if guildID == "" {
		return nil, ErrNotInGuild
	}
	if len(allowed) == 0 {
		return nil, ErrNoPublicChannels
	}
	if len(terms) == 0 {
		// 語が取れない質問（「教えてください」だけ等）は投げても0件
		return nil, nil
	}
	names := make(map[string]string, len(allowed))
	for _, channel := range allowed {
		names[channel.ID] = channel.Name
	}

	f := &fetcher{token: botToken}
	// ⚠️ **語ごとに別のクエリを投げる。** 1つにまとめるとAND条件になり、
	// 「荷重試験 申請」で21件が1件になる（docs/08 M64）
	found := make([][]Message, 0, len(terms))
	for _, term := range terms {
		hits, err := searchOnce(ctx, f, guildID, term, allowed)
		if err != nil {
			continue // 1語が失敗してもほかの語は探せる
		}
		found = append(found, hits)
	}
	return withContext(ctx, f, interleave(found), names)
}

// interleave は語ごとの結果を、1件ずつ順番に取り出して束ねる。
//
// ⚠️ **後ろの語を、先頭の語に食わせない。** 語を分けて投げるのはANDで
// 潰さないためだが（docs/08 M64）、結果を語ごとに前から並べると、1語目が
// 上限の25件を返した時点で MaxSearchHits の12枠が埋まり、**2語目以降が
// 1件も読まれない**。分けて投げた意味が結果側で消える。
func interleave(groups [][]Message) []Message {
	var out []Message
	seen := map[string]bool{}
	for round := 0; ; round++ {
		remaining := false
		for _, group := range groups {
			if round >= len(group) {
				continue
			}
			remaining = true
			if hit := group[round]; !seen[hit.ID] {
				seen[hit.ID] = true
				out = append(out, hit)
			}
		}
		if !remaining {
			return out
		}
	}
}

// searchOnce は1語ぶんの検索。
//
// ⚠️ **チャンネルを明示して投げる。** 省略すると、ボットが見える非公開
// チャンネルまで対象になる（画面の利用者はDiscordのメンバーとは限らない）。
func searchOnce(ctx context.Context, f *fetcher, guildID, term string, allowed []Channel) ([]Message, error) {
	values := url.Values{}
	values.Set("content", strings.TrimSpace(term))
	for _, channel := range allowed {
		values.Add("channel_id", channel.ID)
	}
	values.Set("limit", fmt.Sprint(SearchLimit))
	values.Set("sort_by", "timestamp")
	values.Set("sort_order", "desc")

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
		if hit, ok := pickHit(group); ok {
			hits = append(hits, hit)
		}
	}
	return hits, nil
}

// pickHit は検索結果の1組から、実際に一致した1件を取り出す。
//
// Discordは「ヒットとその前後」を組で返す。組の中で `hit` が立っているものが本体で、
// 立っていなければ（仕様変更などで）先頭を使う。
func pickHit(group []Message) (Message, bool) {
	for _, message := range group {
		if message.Hit {
			return message, true
		}
	}
	if len(group) > 0 {
		return group[0], true
	}
	return Message{}, false
}

// withContext はヒットの前後を取って、チャンネルごとの会話へ組み直す。
func withContext(ctx context.Context, f *fetcher, hits []Message, names map[string]string) ([]ChannelLog, error) {
	byChannel := map[string][]Message{}
	seen := map[string]bool{}
	// **読めたヒットの数で止める。** 捨てたぶんも数えると、許可外の
	// チャンネルのヒットが先頭に並んだだけで枠を使い切ってしまう
	taken := 0
	for _, hit := range hits {
		if taken >= MaxSearchHits {
			break
		}
		channelID := hit.ChannelID
		if _, ok := names[channelID]; !ok {
			continue // 許可していないチャンネルのヒットは捨てる
		}
		taken++
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
		sort.Slice(messages, func(a, b int) bool {
			return messages[a].Timestamp.After(messages[b].Timestamp)
		})
		logs = append(logs, ChannelLog{Channel: names[channelID], ID: channelID, Messages: messages})
	}
	// 見出しの順を毎回同じにする。走るたびに並びが変わると差分が読めない
	sort.Slice(logs, func(a, b int) bool { return logs[a].Channel < logs[b].Channel })
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

// MessageURL は発言そのものを開くURL。
//
// **これが出典になる。** Discordの会話は索引のページではないが、
// 「どこを開けば確かめられるか」は示せる。示さないと、部員の発言を根拠に
// 答えたことが後から誰にも追えない（2026-09-13に本番で発覚）。
func MessageURL(guildID, channelID, messageID string) string {
	return fmt.Sprintf("https://discord.com/channels/%s/%s/%s", guildID, channelID, messageID)
}

// ChannelURL はチャンネルを開くURL。発言が特定できないときに使う。
func ChannelURL(guildID, channelID string) string {
	return fmt.Sprintf("https://discord.com/channels/%s/%s", guildID, channelID)
}

// SearchScope は画面へ出す「何を読んだか」の説明。
func SearchScope(terms []string, channels, messages int) string {
	return fmt.Sprintf("Discordの公開チャンネル%d件から「%s」を検索し、前後を含む%d件の発言を読みました",
		channels, strings.Join(terms, "」「"), messages)
}
