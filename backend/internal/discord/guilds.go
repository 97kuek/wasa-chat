package discord

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// guildCacheTTL はボットが入っているサーバー一覧を覚えておく時間。
//
// サーバーの出入りは代替わりのときぐらいしか起きないので、長めでよい。
// 短くしても、質問のたびに1リクエスト増えるだけで得がない。
const guildCacheTTL = 10 * time.Minute

// MaxSearchGuilds は1回の質問で検索するサーバーの数。
//
// **上限は要る。** 代が進むほどサーバーが増えるので、「すべて」を選ばれたときに
// 際限なくリクエストが増える。新しい代から順に、ここまで。
const MaxSearchGuilds = 3

// Guild はボットが入っているサーバー。
type Guild struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var (
	guildCacheMu sync.Mutex
	guildCache   struct {
		guilds []Guild
		at     time.Time
	}
)

// ListGuilds はボットが入っているサーバーを返す。
//
// **代ごとにDiscordのサーバーが変わる**ため、設定で1つに固定できない
// （2026-09-13の指摘）。ボットが入っている先をそのまま選択肢にする。
func ListGuilds(ctx context.Context, botToken string) []Guild {
	if botToken == "" {
		return nil
	}
	guildCacheMu.Lock()
	if time.Since(guildCache.at) < guildCacheTTL {
		found := guildCache.guilds
		guildCacheMu.Unlock()
		return found
	}
	guildCacheMu.Unlock()

	var guilds []Guild
	if err := (&fetcher{token: botToken}).get(ctx, apiBase+"/users/@me/guilds", &guilds); err != nil {
		// 一覧が取れないだけ。**古い一覧を返すより空を返す**
		// （消えたサーバーを選ばせて、後で失敗させるほうが分かりにくい）
		return nil
	}
	// 名前順で安定させる。並びが毎回変わると、選び直すたびに位置が動く
	sort.Slice(guilds, func(i, j int) bool { return guilds[i].Name < guilds[j].Name })

	guildCacheMu.Lock()
	guildCache.guilds, guildCache.at = guilds, time.Now()
	guildCacheMu.Unlock()
	return guilds
}

// ResetGuildCache はテスト用。本番からは呼ばない。
func ResetGuildCache() {
	guildCacheMu.Lock()
	guildCache.guilds, guildCache.at = nil, time.Time{}
	guildCacheMu.Unlock()
}

// GuildLabel はサーバーの呼び名。一覧に無ければIDをそのまま返す。
func GuildLabel(guilds []Guild, id string) string {
	for _, guild := range guilds {
		if guild.ID == id {
			return guild.Name
		}
	}
	return id
}

// SearchScopeAcross は複数サーバーを検索したときの説明。
func SearchScopeAcross(terms []string, servers []string, channels, messages int) string {
	where := strings.Join(servers, "・")
	if len(servers) == 0 {
		where = "Discord"
	}
	// **探した語をそのまま出す。** 質問と違う語で探していることがあるので
	// （「最近のは？」→前の質問の語）、何で探したのかが分からないと結果を読めない
	return fmt.Sprintf("%s の公開チャンネル%d件から「%s」を検索し、前後を含む%d件の発言を読みました",
		where, channels, strings.Join(terms, "」「"), messages)
}

// SetAPIBaseForTest は宛先を差し替える。**テストからしか呼ばない。**
// 空文字を渡すと本来の宛先へ戻る。
func SetAPIBaseForTest(base string) {
	if base == "" {
		apiBase = defaultAPIBase
		return
	}
	apiBase = base
}
