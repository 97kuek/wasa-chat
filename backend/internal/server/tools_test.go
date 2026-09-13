package server

import (
	"testing"

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

// Discordは公開チャンネルだけを読むので、許可では絞らない
func TestDiscordNeedsNoPermission(t *testing.T) {
	srv := toolServer(t, false)
	srv.cfg.DiscordBotToken = "token"
	srv.cfg.DiscordGuildID = "g1"
	for _, tool := range srv.tools(t.Context(), "部員A") {
		if tool.ID == pipeline.ToolDiscord && !tool.Available {
			t.Fatalf("Discordに許可を要求している: %+v", tool)
		}
	}
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
