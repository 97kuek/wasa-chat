package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/97kuek/wasa-chat/backend/internal/discord"
	"github.com/97kuek/wasa-chat/backend/internal/pipeline"
	"github.com/97kuek/wasa-chat/backend/internal/state"
)

// settingsServer はボットが**連携の上限より多い**サーバーに入っている状態を作る。
func settingsServer(t *testing.T) *Server {
	t.Helper()
	srv := toolServer(t, true)
	srv.cfg.DiscordBotToken = "token"
	srv.cfg.DiscordAppID = "app"
	srv.cfg.CalendarIDs = []string{"wasa@group.calendar.google.com"}
	guilds := make([]discord.Guild, 0, discord.MaxSearchGuilds+1)
	for i := 0; i <= discord.MaxSearchGuilds; i++ {
		guilds = append(guilds, discord.Guild{
			ID: fmt.Sprintf("g4%d", i+1), Name: fmt.Sprintf("WASA %d代", 41+i),
			// 名前以外の見分け（アイコン・人数）も返る状態にする
			Icon: "iconhash", MemberCount: 40 + i,
		})
	}
	stub := &discordGuildStub{guilds: guilds}
	stub.serve(t)
	return srv
}

func saveSettings(t *testing.T, srv *Server, user, body string) *httptest.ResponseRecorder {
	t.Helper()
	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, srv.testRequest("PUT", "/api/settings", body, user))
	return res
}

func readSettings(t *testing.T, srv *Server, user string) SettingsView {
	t.Helper()
	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, srv.testRequest("GET", "/api/settings", "", user))
	if res.Code != http.StatusOK {
		t.Fatalf("設定を読めない: %d %s", res.Code, res.Body)
	}
	var view SettingsView
	if err := json.Unmarshal(res.Body.Bytes(), &view); err != nil {
		t.Fatalf("設定を解釈できない: %v %s", err, res.Body)
	}
	return view
}

// ⚠️ **代ごとにDiscordのサーバーが変わり、つなぐ先は利用者ごとに違う**
// （2026-09-14の指摘）。連携した人だけ、そのサーバーの会話を読む
func TestDiscordConnectionsArePerUser(t *testing.T) {
	srv := settingsServer(t)

	if res := saveSettings(t, srv, "部員A", `{"tools":[],"discordServers":["g42"]}`); res.Code != http.StatusOK {
		t.Fatalf("連携できない: %d %s", res.Code, res.Body)
	}

	mine := readSettings(t, srv, "部員A")
	if len(mine.Discord.Connected) != 1 || mine.Discord.Connected[0].ID != "g42" {
		t.Fatalf("連携したサーバーが残っていない: %+v", mine.Discord)
	}
	// 残りは「まだ連携していない」側に出る。**黙って消さない**
	if len(mine.Discord.Joinable) != discord.MaxSearchGuilds {
		t.Fatalf("追加できるサーバーが出ていない: %+v", mine.Discord.Joinable)
	}

	// ほかの利用者には効かない
	other := readSettings(t, srv, "部員B")
	if len(other.Discord.Connected) != 0 {
		t.Fatalf("他人の連携が見えている: %+v", other.Discord.Connected)
	}
}

// 連携・解除・つなぎ替えが同じ口でできる（画面はどれも保存として扱う）
func TestDiscordConnectionsCanBeChangedAndRemoved(t *testing.T) {
	srv := settingsServer(t)
	const user = "部員A"

	saveSettings(t, srv, user, `{"discordServers":["g41"]}`)
	saveSettings(t, srv, user, `{"discordServers":["g42","g43"]}`) // つなぎ替え
	after := readSettings(t, srv, user)
	if len(after.Discord.Connected) != 2 {
		t.Fatalf("つなぎ替えできていない: %+v", after.Discord.Connected)
	}

	if res := saveSettings(t, srv, user, `{"discordServers":[]}`); res.Code != http.StatusOK {
		t.Fatalf("解除できない: %d %s", res.Code, res.Body)
	}
	if got := readSettings(t, srv, user); len(got.Discord.Connected) != 0 {
		t.Fatalf("解除しても残っている: %+v", got.Discord.Connected)
	}
}

// ⚠️ **画面から届いた値をそのまま保存しない。** ボットが入っていないサーバーや、
// 上限を超えた指定は断る（断らないと、設定画面に出ないのに保存先だけに残る）
func TestSettingsRejectUnknownAndTooManyGuilds(t *testing.T) {
	srv := settingsServer(t)

	unknown := saveSettings(t, srv, "部員A", `{"discordServers":["入っていないサーバー"]}`)
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("ボットが入っていないサーバーを保存した: %d %s", unknown.Code, unknown.Body)
	}

	// **ボットが入っているサーバーだけを並べる。** 断る理由を上限に限定する
	ids := make([]string, 0, discord.MaxSearchGuilds+1)
	for i := 0; i <= discord.MaxSearchGuilds; i++ {
		ids = append(ids, fmt.Sprintf("g4%d", i+1))
	}
	payload, _ := json.Marshal(map[string]any{"discordServers": ids})
	tooMany := saveSettings(t, srv, "部員A", string(payload))
	if tooMany.Code != http.StatusBadRequest {
		t.Fatalf("上限を超えて保存できた: %d %s", tooMany.Code, tooMany.Body)
	}
	if got := readSettings(t, srv, "部員A"); len(got.Discord.Connected) != 0 {
		t.Fatalf("断ったのに保存されている: %+v", got.Discord.Connected)
	}
}

// ⚠️ **Discordのオン・オフは持たない。** 連携したサーバーの有無がそのまま
// オン・オフである。両方を持つと「オンなのに何も読まない」状態が作れる
func TestDiscordIsEnabledByHavingConnections(t *testing.T) {
	none := enabledTools(state.UserSettings{Tools: []string{pipeline.ToolCalendar}})
	for _, tool := range none {
		if tool == pipeline.ToolDiscord {
			t.Fatalf("連携していないのにDiscordを読もうとしている: %v", none)
		}
	}
	// 古い文書に discord が残っていても、連携が無ければ読まない
	stale := enabledTools(state.UserSettings{Tools: []string{pipeline.ToolDiscord}})
	if len(stale) != 0 {
		t.Fatalf("古い設定でDiscordが有効になった: %v", stale)
	}
	connected := enabledTools(state.UserSettings{DiscordGuilds: []string{"g42"}})
	if len(connected) != 1 || connected[0] != pipeline.ToolDiscord {
		t.Fatalf("連携したのにDiscordを読まない: %v", connected)
	}
}

// 保存できるのは知っている参照先だけ。知らないIDは落とす
func TestSettingsKeepOnlyKnownTools(t *testing.T) {
	srv := settingsServer(t)
	if res := saveSettings(t, srv, "主管理者",
		`{"tools":["drive","まだ知らない道具","discord"]}`); res.Code != http.StatusOK {
		t.Fatalf("保存できない: %d %s", res.Code, res.Body)
	}
	saved := srv.userSettings(t.Context(), srv.userKey("主管理者"))
	if len(saved.Tools) != 1 || saved.Tools[0] != pipeline.ToolDrive {
		t.Fatalf("知らない参照先まで保存した: %v", saved.Tools)
	}
}

// **使えないものも出し、理由を書く。** 一覧から消すと「無い機能」に見える
func TestSettingsShowUnavailableToolsWithReason(t *testing.T) {
	srv := settingsServer(t)
	view := readSettings(t, srv, "部員A") // 共有ドライブの許可が無い利用者

	found := false
	for _, tool := range view.Tools {
		if tool.ID != pipeline.ToolDrive {
			continue
		}
		found = true
		if tool.Available || tool.Reason == "" {
			t.Fatalf("使えない理由が伝わらない: %+v", tool)
		}
	}
	if !found {
		t.Fatal("使えない参照先を一覧から消している")
	}
}

// ⚠️ **名前だけを並べても選べない。** 「WASA 41代」「WASA 42代」と4つ出ても、
// 初めて設定する人にはどれが自分のサーバーか分からない（2026-09-14の指摘）
func TestSettingsDescribeServersSoTheyCanBeTold(t *testing.T) {
	srv := settingsServer(t)
	view := readSettings(t, srv, "部員A")

	if len(view.Discord.Joinable) == 0 {
		t.Fatal("選べるサーバーが出ていない")
	}
	got := view.Discord.Joinable[0]
	if got.Members == 0 {
		t.Fatalf("参加人数が出ていない: %+v", got)
	}
	if got.Channels == 0 {
		t.Fatalf("検索する公開チャンネルの数が出ていない: %+v", got)
	}
	// アイコンは data URI で渡す（CSPが img-src 'self' data: のため）
	if !strings.HasPrefix(got.Icon, "data:image/png;base64,") {
		t.Fatalf("アイコンを画面へ渡せていない: %q", got.Icon)
	}
}

// ボットの追加は画面から押せる。コマンドを打てる人しか増やせない作りにしない
func TestSettingsOfferBotInvite(t *testing.T) {
	view := readSettings(t, settingsServer(t), "部員A")
	if view.Discord.InviteURL == "" {
		t.Fatal("ボットを追加する入口が無い")
	}
	if view.Discord.MaxServers != discord.MaxSearchGuilds {
		t.Fatalf("連携できる数が伝わらない: %+v", view.Discord)
	}
}

// 未ログインでは読み書きできない
func TestSettingsRequireLogin(t *testing.T) {
	srv := settingsServer(t)
	for _, method := range []string{"GET", "PUT"} {
		res := httptest.NewRecorder()
		srv.Routes().ServeHTTP(res, httptest.NewRequest(method, "/api/settings", nil))
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("未ログインで %s できた: %d", method, res.Code)
		}
	}
}
