package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/97kuek/wasa-chat/backend/internal/discord"
	"github.com/97kuek/wasa-chat/backend/internal/pipeline"
	"github.com/97kuek/wasa-chat/backend/internal/state"
)

// 設定画面（外部サービス連携）。
//
// ⚠️ **入力欄の「+」ではなく設定画面に置く。** 連携は質問のたびに切り替える
// ものではなく、一度つないだら続くものである。質問の直前に決める作りだと、
// つなぐ・外すという操作の置き場所が無い（2026-09-14に人間が判断）。
//
// ⚠️ **Discordは利用者ごとに違う。** 代ごとにサーバーが変わり、どの代の会話を
// 読みたいかは人によって違う。設定に1つ書いて全員に効かせることはできない。

// maxSettingsBodyBytes は設定の保存で受け取る上限。
// 入っているのはIDの配列だけなので、質問本文より桁で小さくてよい。
const maxSettingsBodyBytes = 16 << 10

// settingsDetailTimeout は、サーバーの見分けが付く材料（アイコン・チャンネル数）を
// 取りに行ける時間。**設定画面をDiscordの応答待ちにしない。**
// 間に合わなければ名前だけで出す。
const settingsDetailTimeout = 5 * time.Second

var (
	// errNotJoinedGuild は、ボットが入っていないサーバーを連携しようとしたとき。
	// **画面が古いだけ**のことがあるので、文言でやることを示す
	errNotJoinedGuild = errors.New("そのサーバーにWASA Chatのボットが入っていません。一覧を更新してください")
	errTooManyGuilds  = fmt.Errorf("連携できるDiscordサーバーは%d個までです", discord.MaxSearchGuilds)
)

// SettingsView は設定画面へ返す、利用者ごとの連携の状態。
type SettingsView struct {
	Tools   []SettingsTool  `json:"tools"`
	Discord DiscordSettings `json:"discord"`
}

// SettingsTool はオン・オフだけで足せる参照先（共有ドライブ・カレンダー）。
//
// ⚠️ **使えないものも返す。** 一覧から消すと「無い機能」に見え、設定すれば
// 使えることが伝わらない（入力欄の「+」のときと同じ理由）。
type SettingsTool struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Available   bool   `json:"available"`
	// Reason は Available が false のときだけ入る。なぜ使えないか
	Reason string `json:"reason,omitempty"`
	// Enabled は利用者がオンにしているか
	Enabled bool `json:"enabled"`
}

// DiscordSettings は連携したDiscordサーバーと、増やすための入口。
//
// ⚠️ **オン・オフのスイッチは持たない。** 連携したサーバーの有無がそのまま
// オン・オフである。両方を持つと「オンなのに1つも連携していない」という、
// 画面ではオンに見えるのに何も読まない状態が作れてしまう。
type DiscordSettings struct {
	// Connected は連携中のサーバー。**ボットがもういないものは出さない**
	// （選び直しようがないものを残すと、外す操作が要る設定に見える）
	Connected []ToolServer `json:"connected"`
	// Joinable はボットが入っていて、まだ連携していないサーバー
	Joinable []ToolServer `json:"joinable"`
	// InviteURL はボットを新しいサーバーへ入れるURL。空なら設定が足りない
	InviteURL string `json:"inviteUrl,omitempty"`
	// MaxServers は同時に連携できるサーバーの数（1回の質問で読む上限と同じ）
	MaxServers int `json:"maxServers"`
	// Reason は連携できないときの理由。ボットトークンが無い場合など
	Reason string `json:"reason,omitempty"`
}

// userSettings は保存してある連携を読む。
//
// ⚠️ **読めないときは何も足さない。** 失敗を「全部オン」に倒すと、
// 一時的な障害のときだけ普段より広い範囲を読む回答が出る。
func (s *Server) userSettings(ctx context.Context, key string) state.UserSettings {
	settings, ok, err := s.state.GetUserSettings(ctx, key)
	if err != nil {
		log.Printf("連携設定を読み込めません: %v", err)
		return state.UserSettings{}
	}
	if !ok {
		return state.UserSettings{}
	}
	return settings
}

// enabledTools は保存してある連携から、この質問で足す参照先を組み立てる。
//
// Discordは Tools ではなく**連携したサーバーの有無**で決まる（DiscordSettings 参照）。
// ここで返すのは利用者の希望であって、使ってよいかは allowedTools が決める。
func enabledTools(settings state.UserSettings) []string {
	tools := make([]string, 0, len(settings.Tools)+1)
	for _, tool := range settings.Tools {
		if tool == pipeline.ToolDiscord {
			continue // 保存時に外しているが、古い文書のために無視しておく
		}
		tools = append(tools, tool)
	}
	if len(settings.DiscordGuilds) > 0 {
		tools = append(tools, pipeline.ToolDiscord)
	}
	return tools
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	user, _ := s.currentUser(r)
	// **ボットを入れた直後に押せる口を用意する。** 一覧は10分覚えているので、
	// 追加して戻ってきた人には、まだ新しいサーバーが見えない
	if r.URL.Query().Get("refresh") == "1" && s.cfg.DiscordBotToken != "" {
		discord.RefreshGuilds(r.Context(), s.cfg.DiscordBotToken)
	}
	writeJSON(w, http.StatusOK, s.settingsView(r.Context(), user))
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	user, _ := s.currentUser(r)
	var body struct {
		// Tools はオンにした参照先。Discordは含めない（サーバーの連携で決まる）
		Tools []string `json:"tools"`
		// DiscordServers は連携するDiscordサーバーのID
		DiscordServers []string `json:"discordServers"`
	}
	if err := decodeJSON(w, r, maxSettingsBodyBytes, &body); err != nil {
		writeJSON(w, invalidJSONStatus(err), map[string]string{"error": "リクエストが不正です"})
		return
	}

	// ⚠️ **画面から届いた値をそのまま保存しない。** 知らないIDや、ボットが
	// 入っていないサーバーを保存すると、設定画面には出ないのに保存先にだけ
	// 残るものができる
	guilds, err := s.connectableGuilds(r.Context(), body.DiscordServers)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	settings := state.UserSettings{
		Tools:         switchableTools(body.Tools),
		DiscordGuilds: guilds,
		UpdatedAt:     time.Now().UTC(),
	}
	if err := s.state.SaveUserSettings(r.Context(), s.userKey(user), settings); err != nil {
		log.Printf("連携設定を保存できません: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "連携を保存できませんでした"})
		return
	}
	// 保存した結果をそのまま返す。**画面の手元の値を正としない**
	// （落としたIDがあるとき、画面だけがオンのままになる）
	writeJSON(w, http.StatusOK, s.settingsView(r.Context(), user))
}

// switchableTools は保存してよい参照先だけを残す。
//
// Discordは**ここに入らない**（連携したサーバーの有無がオン・オフである）。
// 使えるかどうかでは絞らない。許可が下りたときに設定し直させないためで、
// 実際に使ってよいかは質問のたびに allowedTools が決める。
func switchableTools(requested []string) []string {
	known := []string{pipeline.ToolDrive, pipeline.ToolCalendar}
	tools := make([]string, 0, len(known))
	for _, tool := range known {
		if slices.Contains(requested, tool) {
			tools = append(tools, tool)
		}
	}
	return tools
}

// connectableGuilds は連携してよいサーバーだけを返す。
//
// ⚠️ **上限を設ける。** 1回の質問で読むサーバーが増えるほどDiscordへの
// リクエストが増える（docs/09 A-9）。上限は1回の質問で読む数と同じにする。
func (s *Server) connectableGuilds(ctx context.Context, requested []string) ([]string, error) {
	if len(requested) == 0 {
		return []string{}, nil
	}
	available := s.searchableGuilds(ctx)
	guilds := make([]string, 0, len(requested))
	for _, id := range requested {
		if slices.Contains(guilds, id) {
			continue // 同じサーバーを二重に連携しない
		}
		if !slices.ContainsFunc(available, func(guild discord.Guild) bool { return guild.ID == id }) {
			return nil, errNotJoinedGuild
		}
		guilds = append(guilds, id)
	}
	if len(guilds) > discord.MaxSearchGuilds {
		return nil, errTooManyGuilds
	}
	return guilds, nil
}

// describeServers は、選ぶ材料を添えたサーバー一覧を作る。
//
// ⚠️ **質問のたびには呼ばない。** アイコンとチャンネル数の取得はDiscordへの
// 追加のリクエストで（どちらも覚えておくが）、回答を遅らせる理由が無い。
// ここは設定画面を開いたときだけ通る。
//
// ⚠️ **取れないものは出さない。** 人数もチャンネル数も、分からないまま
// 「0人」と出すと、閉じているサーバーに見える。
func (s *Server) describeServers(ctx context.Context, guilds []discord.Guild) []ToolServer {
	ctx, cancel := context.WithTimeout(ctx, settingsDetailTimeout)
	defer cancel()

	// **まとめて取りに行く。** 代が増えるほど順番待ちが積み上がり、
	// 設定画面を開くたびに待たされる
	servers := make([]ToolServer, len(guilds))
	var wait sync.WaitGroup
	for i, guild := range guilds {
		wait.Add(1)
		go func() {
			defer wait.Done()
			servers[i] = ToolServer{
				ID:       guild.ID,
				Name:     guild.Name,
				Icon:     discord.IconDataURL(ctx, guild),
				Members:  guild.MemberCount,
				Channels: len(s.searchableChannels(ctx, guild.ID)),
			}
		}()
	}
	wait.Wait()
	return servers
}

// settingsView は設定画面に出す形を組み立てる。
//
// **使えるかどうかはサーバーが決める。** 画面に固定で並べると、未設定のものが
// 押せてしまい「つないだのに効かない」になる。
func (s *Server) settingsView(ctx context.Context, user string) SettingsView {
	saved := s.userSettings(ctx, s.userKey(user))
	view := SettingsView{
		Tools:   []SettingsTool{},
		Discord: DiscordSettings{Connected: []ToolServer{}, Joinable: []ToolServer{}, MaxServers: discord.MaxSearchGuilds},
	}
	for _, tool := range s.tools(ctx, user) {
		if tool.ID == pipeline.ToolDiscord {
			view.Discord.Reason = tool.Reason
			view.Discord.InviteURL = discordInviteURL(s.cfg.DiscordAppID)
			for _, server := range s.describeServers(ctx, s.searchableGuilds(ctx)) {
				if slices.Contains(saved.DiscordGuilds, server.ID) {
					view.Discord.Connected = append(view.Discord.Connected, server)
					continue
				}
				view.Discord.Joinable = append(view.Discord.Joinable, server)
			}
			continue
		}
		view.Tools = append(view.Tools, SettingsTool{
			ID:          tool.ID,
			Name:        tool.Name,
			Description: tool.Description,
			Available:   tool.Available,
			Reason:      tool.Reason,
			Enabled:     slices.Contains(saved.Tools, tool.ID),
		})
	}
	return view
}
