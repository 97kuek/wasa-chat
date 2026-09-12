package discord

import (
	"context"
	"strings"
	"sync"
	"time"
)

// channelCacheTTL は公開チャンネル一覧を覚えておく時間。
//
// ⚠️ **補完は3秒以内に同期で返さないと消える。**「考えています」で先延ばしできない。
// 打つたびにDiscordへ問い合わせると、文字を打つ速さに間に合わない上に
// レート制限にも当たる。**短く覚える**のが素直な解。
//
// 覚えすぎると、新しく作ったチャンネルが候補に出ない。1分なら、作った人が
// 「出ないな」と思って打ち直す間に入れ替わる。
const channelCacheTTL = time.Minute

type cachedChannels struct {
	channels []Channel
	at       time.Time
}

var (
	channelCacheMu sync.Mutex
	channelCache   = map[string]cachedChannels{}
)

// ScopeChoices は「範囲」オプションの候補を作る。
//
// 先頭は必ず「公開チャンネル全部」。その後ろに公開チャンネルを画面順で並べ、
// 打った文字で絞り込む。**非公開チャンネルは候補に出さない**
// （Channel.public の説明を参照）。
func ScopeChoices(ctx context.Context, botToken, guildID, typed string) []Choice {
	choices := make([]Choice, 0, AutocompleteLimit)
	typed = strings.ToLower(strings.TrimSpace(typed))

	const allLabel = "公開チャンネル全部"
	if typed == "" || strings.Contains(allLabel, typed) || strings.HasPrefix("all", typed) {
		choices = append(choices, Choice{Name: allLabel, Value: ScopeAllChannels})
	}
	if guildID == "" {
		return choices
	}
	for _, channel := range cachedPublicChannels(ctx, botToken, guildID) {
		if len(choices) >= AutocompleteLimit {
			break
		}
		if typed != "" && !strings.Contains(strings.ToLower(channel.Name), typed) {
			continue
		}
		choices = append(choices, Choice{Name: "#" + channel.Name, Value: channel.ID})
	}
	return choices
}

func cachedPublicChannels(ctx context.Context, botToken, guildID string) []Channel {
	channelCacheMu.Lock()
	if hit, ok := channelCache[guildID]; ok && time.Since(hit.at) < channelCacheTTL {
		channelCacheMu.Unlock()
		return hit.channels
	}
	channelCacheMu.Unlock()

	found, err := publicChannels(ctx, &fetcher{token: botToken}, guildID)
	if err != nil {
		// 候補が出ないだけで、コマンド自体は打てる。ここで失敗を見せない
		return nil
	}
	channelCacheMu.Lock()
	channelCache[guildID] = cachedChannels{channels: found, at: time.Now()}
	channelCacheMu.Unlock()
	return found
}

// PublicChannel は指定のチャンネルが、そのサーバーの公開チャンネルなら返す。
//
// ⚠️ **補完で出したものだけが選ばれるとは限らない。** 「範囲」は文字列の
// オプションなので、利用者は候補に無いチャンネルIDを手で打てる。
// 非公開チャンネルのIDを打たれても読まないよう、実行前にここで確かめる。
func PublicChannel(ctx context.Context, botToken, guildID, channelID string) (Channel, bool) {
	for _, channel := range cachedPublicChannels(ctx, botToken, guildID) {
		if channel.ID == channelID {
			return channel, true
		}
	}
	return Channel{}, false
}
