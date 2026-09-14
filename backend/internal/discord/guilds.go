package discord

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
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
// 際限なくリクエストが増える。
//
// ⚠️ **どの3つが選ばれるかは名前順である**（ListGuilds の並び）。
// 「新しい代から」ではない。代の新しさはサーバー名からしか分からず、
// 命名は代ごとにまちまちなので、名前から推測する規則は入れていない。
// 代を指定したいときは、画面のサーバー選択で明示する。
const MaxSearchGuilds = 3

// Guild はボットが入っているサーバー。
//
// ⚠️ **名前だけでは選べない。** 「WASA 41代」「WASA 42代」と並んでも、初めて
// 設定する人には**どれが自分のいるサーバーか分からない**（2026-09-14の指摘）。
// 見分けが付く材料（アイコン・人数）を一緒に持つ。
type Guild struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Icon はDiscordのアイコンのハッシュ。空なら設定されていない
	Icon string `json:"icon"`
	// MemberCount はおおよその参加人数（with_counts で取れる）
	MemberCount int `json:"approximate_member_count"`
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
	// with_counts を付けると参加人数が一緒に返る。**リクエストは増えない**
	if err := (&fetcher{token: botToken}).get(ctx, apiBase+"/users/@me/guilds?with_counts=true", &guilds); err != nil {
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

// アイコンは**中継してから画面へ渡す**。
//
// ⚠️ **画面から cdn.discordapp.com を直接読めない。** CSPが
// `img-src 'self' data:` なので、外部ホストの画像は表示できない
// （docs/04）。アシスタントのアイコンと同じく data URI にして渡す。
const (
	iconCacheTTL = time.Hour
	// アイコンは64pxのPNG。**上限を設ける**（読み込む側の上限でもある）
	maxIconBytes = 64 << 10
	iconTimeout  = 2 * time.Second
)

var cdnBase = "https://cdn.discordapp.com"

var (
	iconCacheMu sync.Mutex
	iconCache   = map[string]cachedIcon{}
)

type cachedIcon struct {
	dataURL string
	at      time.Time
}

// IconDataURL はサーバーのアイコンを data URI で返す。取れなければ空。
//
// ⚠️ **BOTトークンを付けない。** 宛先はDiscord APIではなく画像の配信元で、
// 認証も要らない。付ければ、トークンを別のホストへ渡すだけになる。
//
// ⚠️ **取れなくても画面は出す。** アイコンは見分けを助けるためのもので、
// 無ければ画面側が名前の頭文字で描く。
func IconDataURL(ctx context.Context, guild Guild) string {
	if guild.Icon == "" {
		return ""
	}
	key := guild.ID + "/" + guild.Icon
	iconCacheMu.Lock()
	if hit, ok := iconCache[key]; ok && time.Since(hit.at) < iconCacheTTL {
		iconCacheMu.Unlock()
		return hit.dataURL
	}
	iconCacheMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, iconTimeout)
	defer cancel()
	url := fmt.Sprintf("%s/icons/%s/%s.png?size=64", cdnBase, guild.ID, guild.Icon)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxIconBytes+1))
	if err != nil || len(body) > maxIconBytes {
		return ""
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(body)

	iconCacheMu.Lock()
	iconCache[key] = cachedIcon{dataURL: dataURL, at: time.Now()}
	iconCacheMu.Unlock()
	return dataURL
}

// SetCDNBaseForTest はアイコンの取得先を差し替える。**テストと手元の確認だけ。**
func SetCDNBaseForTest(base string) {
	if base == "" {
		cdnBase = "https://cdn.discordapp.com"
		return
	}
	cdnBase = base
	iconCacheMu.Lock()
	iconCache = map[string]cachedIcon{}
	iconCacheMu.Unlock()
}

// RefreshGuilds は覚えていた一覧を捨てて取り直す。
//
// **ボットを入れた直後のために要る。** 一覧は10分覚えているので、設定画面で
// 「サーバーへ追加」を押して戻ってきた人には、追加したサーバーがまだ見えない。
// 「入れたのに出てこない」は、入れ直しを何度も試させることになる
// （2026-09-14の指摘）。押されたときだけ取り直す。
func RefreshGuilds(ctx context.Context, botToken string) []Guild {
	ResetGuildCache()
	return ListGuilds(ctx, botToken)
}

// ResetGuildCache は覚えていた一覧を捨てる。テストと RefreshGuilds から呼ぶ。
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
