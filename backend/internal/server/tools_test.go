package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/97kuek/wasa-chat/backend/internal/discord"
	"github.com/97kuek/wasa-chat/backend/internal/index"
	"github.com/97kuek/wasa-chat/backend/internal/pipeline"
	"github.com/97kuek/wasa-chat/backend/internal/state"
)

func toolServer(t *testing.T, withDrive bool) *Server {
	t.Helper()
	page := `{"pages":[{"id":"1","source":"wiki","title":"荷重試験","chunks":[{"id":"p1-c1","text":"本文","chars":2}]}]}`
	toc := "# WASA 資料の目次\n\n## 引き継ぎWiki（部内限定）全1ページ\n\n- 荷重試験\n"
	if withDrive {
		toc += "\n## 共有ドライブ（部内限定）全1ファイル\n\n- 40代 総会議事録\n"
	}
	ix, err := index.Build([]byte(page), []byte(toc))
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		cfg:   Config{SessionSecret: "テスト用の固定鍵テスト用の固定鍵", AdminUsers: []string{"主管理者"}},
		live:  index.NewLive(ix, "test"),
		state: state.NewMemory(),
	}
}

// ⚠️ **共有ドライブは部内資料より緩い場所である。** Wikiに書かない人が置いた
// 資料が入るため、誰が読めるかを個別に決める（2026-09-13にPMが判断）
func TestDriveNeedsPermission(t *testing.T) {
	srv := toolServer(t, true)
	find := func(user string) Tool {
		for _, tool := range srv.tools(t.Context(), user) {
			if tool.ID == pipeline.ToolDrive {
				return tool
			}
		}
		t.Fatal("共有ドライブが一覧に無い")
		return Tool{}
	}

	if got := find("部員A"); got.Available {
		t.Fatal("許可していない利用者が共有ドライブを使える")
	} else if got.Reason != "管理者の許可が要ります" {
		t.Fatalf("理由が伝わらない: %q", got.Reason)
	}
	// 管理者は共有フォルダの中身を決める側なので、指定しなくても読める
	if got := find("主管理者"); !got.Available {
		t.Fatalf("管理者が使えない: %+v", got)
	}

	// 許可を出すと使える
	if err := srv.state.SaveAdminRole(t.Context(), srv.userKey("部員A"), state.AdminRole{
		Username: "部員A", Tools: []string{pipeline.ToolDrive},
	}); err != nil {
		t.Fatal(err)
	}
	if got := find("部員A"); !got.Available {
		t.Fatalf("許可しても使えない: %+v", got)
	}
}

// **画面の指定だけを信じない。** 許可していない利用者が tools を送ってきても通さない
func TestAllowedToolsIgnoresUnauthorized(t *testing.T) {
	srv := toolServer(t, true)

	got := srv.allowedTools(t.Context(), "部員A", []string{pipeline.ToolDrive, "まだ知らない道具"})
	if len(got) != 0 {
		t.Fatalf("許可していない参照先を通した: %v", got)
	}
	if got := srv.allowedTools(t.Context(), "主管理者", []string{pipeline.ToolDrive}); len(got) != 1 {
		t.Fatalf("管理者の指定を落とした: %v", got)
	}
	// ログインしていない状態では誰も使えない
	if got := srv.allowedTools(t.Context(), "", []string{pipeline.ToolDrive}); len(got) != 0 {
		t.Fatalf("未ログインで通した: %v", got)
	}
}

// 索引に共有ドライブが入っていなければ、許可があっても使えない
func TestDriveUnavailableWithoutIndex(t *testing.T) {
	srv := toolServer(t, false)
	for _, tool := range srv.tools(t.Context(), "主管理者") {
		if tool.ID != pipeline.ToolDrive {
			continue
		}
		if tool.Available {
			t.Fatal("索引に無いのに使えることになっている")
		}
		if tool.Reason != "索引に共有ドライブの資料が入っていません" {
			t.Fatalf("理由が違う: %q", tool.Reason)
		}
	}
}

// Discordは公開チャンネルだけを読むので、許可では絞らない。
// ⚠️ **代ごとにサーバーが変わる**ので、選択肢はボットが入っている先から作る
func TestDiscordNeedsNoPermissionAndListsServers(t *testing.T) {
	srv := toolServer(t, false)
	srv.cfg.DiscordBotToken = "token"
	stub := &discordGuildStub{guilds: []discord.Guild{
		{ID: "g41", Name: "WASA 41代"}, {ID: "g40", Name: "WASA 40代"},
	}}
	stub.serve(t)

	for _, tool := range srv.tools(t.Context(), "部員A") {
		if tool.ID != pipeline.ToolDiscord {
			continue
		}
		if !tool.Available {
			t.Fatalf("Discordに許可を要求している: %+v", tool)
		}
		if len(tool.Servers) != 2 {
			t.Fatalf("サーバーを選べない: %+v", tool.Servers)
		}
		// 名前順で安定させる。並びが毎回変わると、選び直すたびに位置が動く
		if tool.Servers[0].Name != "WASA 40代" {
			t.Fatalf("並び順が安定していない: %+v", tool.Servers)
		}
	}
}

// DISCORD_GUILD_IDS を設定したら、そのサーバーだけへ絞る
func TestSearchableGuildsRespectsAllowList(t *testing.T) {
	srv := toolServer(t, false)
	srv.cfg.DiscordBotToken = "token"
	srv.cfg.DiscordGuildIDs = []string{"g41"}
	stub := &discordGuildStub{guilds: []discord.Guild{
		{ID: "g41", Name: "WASA 41代"}, {ID: "g40", Name: "WASA 40代"},
	}}
	stub.serve(t)

	got := srv.searchableGuilds(t.Context())
	if len(got) != 1 || got[0].ID != "g41" {
		t.Fatalf("許可リストが効いていない: %+v", got)
	}
}

// 指定が無ければ横断するが、**代が進むほどサーバーが増える**ので上限を設ける
func TestSearchTargetsCapsGuilds(t *testing.T) {
	srv := toolServer(t, false)
	srv.cfg.DiscordBotToken = "token"
	guilds := make([]discord.Guild, 6)
	for i := range guilds {
		guilds[i] = discord.Guild{ID: fmt.Sprintf("g%d", i), Name: fmt.Sprintf("WASA %d代", 36+i)}
	}
	stub := &discordGuildStub{guilds: guilds}
	stub.serve(t)

	if got := srv.searchTargets(t.Context(), ""); len(got) != MaxSearchGuilds {
		t.Fatalf("上限が効いていない: %d件", len(got))
	}
	// 指定があればそれだけ
	if got := srv.searchTargets(t.Context(), "g2"); len(got) != 1 || got[0].ID != "g2" {
		t.Fatalf("指定したサーバーを選べない: %+v", got)
	}
	// 許可していないIDを送られても何も読まない
	if got := srv.searchTargets(t.Context(), "入っていないサーバー"); len(got) != 0 {
		t.Fatalf("許可外のサーバーを読もうとしている: %+v", got)
	}
}

// discordGuildStub は `GET /users/@me/guilds` だけを真似る。
type discordGuildStub struct{ guilds []discord.Guild }

func (d *discordGuildStub) serve(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v10/users/@me/guilds", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(d.guilds)
	})
	server := httptest.NewServer(mux)
	discord.SetAPIBaseForTest(server.URL + "/api/v10")
	discord.ResetGuildCache()
	t.Cleanup(func() {
		server.Close()
		discord.SetAPIBaseForTest("")
		discord.ResetGuildCache()
	})
}

// ⚠️ **文書ごと消さない。** 共同管理者を外しただけで共有ドライブの許可まで
// 消えてはいけない（同じ文書に両方が入っている）
func TestRevokingCoAdminKeepsToolGrant(t *testing.T) {
	srv := toolServer(t, true)
	key := srv.userKey("部員A")
	if err := srv.state.SaveAdminRole(t.Context(), key, state.AdminRole{
		Username: "部員A", Role: "co_admin", Tools: []string{pipeline.ToolDrive},
	}); err != nil {
		t.Fatal(err)
	}
	grant, _, _ := srv.state.GetAdminRole(t.Context(), key)
	if err := srv.saveOrClearGrant(t.Context(), key, grant, ""); err != nil {
		t.Fatal(err)
	}

	after, ok, _ := srv.state.GetAdminRole(t.Context(), key)
	if !ok {
		t.Fatal("文書ごと消えた。共有ドライブの許可まで失われる")
	}
	if after.Role != "" {
		t.Fatalf("共同管理者を外せていない: %+v", after)
	}
	if !srv.mayUseDrive(t.Context(), "部員A") {
		t.Fatal("共有ドライブの許可が消えた")
	}

	// 両方無くなったら文書ごと消す（空の行を一覧に残さない）
	after.Tools = nil
	if err := srv.saveOrClearGrant(t.Context(), key, after, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := srv.state.GetAdminRole(t.Context(), key); ok {
		t.Fatal("空の文書が残っている")
	}
}

// 共同管理者も共有フォルダの中身を決める側なので、指定しなくても読める。
// **読み取りは1回にまとめる**（同じ文書に両方入っているため）
func TestCoAdminMayUseDrive(t *testing.T) {
	srv := toolServer(t, true)
	if err := srv.state.SaveAdminRole(t.Context(), srv.userKey("部員A"), state.AdminRole{
		Username: "部員A", Role: "co_admin",
	}); err != nil {
		t.Fatal(err)
	}
	if !srv.mayUseDrive(t.Context(), "部員A") {
		t.Fatal("共同管理者が共有ドライブを使えない")
	}
	if !srv.mayUseDrive(t.Context(), "主管理者") {
		t.Fatal("主管理者が共有ドライブを使えない")
	}
	if srv.mayUseDrive(t.Context(), "部員B") {
		t.Fatal("許可していない利用者を通した")
	}
}

// ⚠️ **Discordの会話も出典として出す。** 索引のページではないが、リンクは作れる。
// 出さないと「Discordの会話によれば」と答えながら、どの発言が根拠なのか
// 後から追えない（2026-09-13に本番で発覚）
func TestDiscordSourceLinksToTheMessage(t *testing.T) {
	hit := discord.Message{ID: "m1", Hit: true, Timestamp: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}
	older := discord.Message{ID: "m0", Timestamp: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}

	got := discordSource("g1", discord.ChannelLog{
		Channel: "WASA42代 / 鳥コン / 全般", ID: "c1", Messages: []discord.Message{hit, older},
	})
	if got.URL != "https://discord.com/channels/g1/c1/m1" {
		t.Fatalf("一致した発言へ飛ばない: %s", got.URL)
	}
	if got.Title != "#WASA42代 / 鳥コン / 全般" {
		t.Fatalf("どのチャンネルか分からない: %s", got.Title)
	}
	if got.Origin != pipeline.ToolDiscord {
		t.Fatalf("資料と区別できない: %s", got.Origin)
	}
	// 最終更新はいちばん新しい発言の日付
	if got.LastEdited != "2026-03-01" {
		t.Fatalf("最終更新が違う: %s", got.LastEdited)
	}

	// 一致した発言が分からなければチャンネルの先頭へ
	noHit := discordSource("g1", discord.ChannelLog{Channel: "雑談", ID: "c2",
		Messages: []discord.Message{older}})
	if noHit.URL != "https://discord.com/channels/g1/c2" {
		t.Fatalf("チャンネルへ飛ばない: %s", noHit.URL)
	}
}

// Discordは参照欄に出すための呼び名を持つが、**索引には入らない**
func TestDiscordIsNotAnIndexOrigin(t *testing.T) {
	if pipeline.KnownOrigin(pipeline.ToolDiscord) {
		t.Fatal("Discordを索引の出所として通している")
	}
	if pipeline.OriginLabel(pipeline.ToolDiscord) != "Discord" {
		t.Fatal("参照欄での呼び名が無い")
	}
}
