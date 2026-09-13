package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/97kuek/wasa-chat/backend/internal/index"
	"github.com/97kuek/wasa-chat/backend/internal/state"
)

type adminRoleReadErrorStore struct {
	*state.Memory
	err error
}

func TestAdminEndpointReportsRoleStoreFailure(t *testing.T) {
	srv, _ := testServer(t, nil)
	srv.state = &adminRoleReadErrorStore{Memory: state.NewMemory(), err: errors.New("読込失敗")}
	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, srv.testRequest(http.MethodGet, "/api/admin/overview", "", "共同管理者"))
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("ロール保存先の障害を権限不足として返した: status=%d body=%s", res.Code, res.Body.String())
	}
}

func (s *adminRoleReadErrorStore) GetAdminRole(context.Context, string) (state.AdminRole, bool, error) {
	return state.AdminRole{}, false, s.err
}

func TestAdminOverviewRequiresAdminRole(t *testing.T) {
	srv, _ := testServer(t, []string{"管理者"})
	cases := []struct {
		name string
		req  *http.Request
		want int
	}{
		{"未ログイン", httptest.NewRequest(http.MethodGet, "/api/admin/overview", nil), http.StatusUnauthorized},
		{"一般利用者", srv.testRequest(http.MethodGet, "/api/admin/overview", "", "一般利用者"), http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := httptest.NewRecorder()
			srv.Routes().ServeHTTP(res, tc.req)
			if res.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", res.Code, tc.want, res.Body.String())
			}
		})
	}
}

func TestAdminOverviewShowsNamesWithoutQuestionContentOrUserKeys(t *testing.T) {
	shared := state.NewMemory()
	srv := &Server{
		cfg: Config{
			SessionSecret: "テスト用の固定鍵テスト用の固定鍵", DailyLimit: 30,
			APIDailyLimit: 500, AdminUsers: []string{"管理者"}, LLMName: "gemini/test-model",
			StoreName: "メモリ", Revision: "test-revision", CodeVersion: "abcdef0",
			IndexPublishedAt: "2026-08-11T01:00:00Z",
		},
		live: index.NewLive(&index.Index{Version: "index1234567"}, "test"), state: shared, startedAt: time.Now().UTC(),
	}
	ctx := context.Background()
	key := srv.userKey("42 Wasa Taro")
	now := time.Now().UTC()
	if err := shared.SaveUserProfile(ctx, key, "42 Wasa Taro", now); err != nil {
		t.Fatal(err)
	}
	if taken, err := shared.Take(ctx, key, today(), 30); err != nil || !taken {
		t.Fatalf("利用回数の下準備に失敗: taken=%v err=%v", taken, err)
	}
	if err := shared.SaveUsageEvent(ctx, state.UsageEvent{
		ID: "event-1", UserKey: key, OccurredAt: now, Outcome: "success", DurationMS: 1200,
	}); err != nil {
		t.Fatal(err)
	}
	pacificDay, _ := pacificDayAndReset(now)
	if reserved, err := shared.ReserveAPIRequest(ctx, pacificDay, "test-model", 500, now); err != nil || !reserved {
		t.Fatalf("API試行を予約できない: reserved=%v err=%v", reserved, err)
	}

	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, srv.testRequest(http.MethodGet, "/api/admin/overview", "", "管理者"))
	if res.Code != http.StatusOK {
		t.Fatalf("管理画面APIが失敗: %d %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, expected := range []string{`"username":"42 Wasa Taro"`, `"today":1`, `"requests":1`, `"actor":"管理者"`, `"codeVersion":"abcdef0"`, `"indexVersion":"index1234567"`, `"updateProgress"`} {
		if !strings.Contains(body, expected) {
			t.Errorf("管理情報に %s がない: %s", expected, body)
		}
	}
	if strings.Contains(body, key) || strings.Contains(body, "質問本文") || strings.Contains(body, "user_key") || strings.Contains(body, "0001-01-01") {
		t.Fatalf("秘密の利用者キーまたは質問本文が管理APIへ出た: %s", body)
	}
}

func TestAdminOverviewTracksPublishAndVerificationProgress(t *testing.T) {
	shared := state.NewMemory()
	checkedAt := time.Date(2026, 8, 11, 1, 0, 0, 0, time.UTC)
	srv := &Server{
		cfg: Config{
			SessionSecret: "テスト用の固定鍵テスト用の固定鍵", DailyLimit: 30,
			APIDailyLimit: 500, AdminUsers: []string{"管理者"}, LLMName: "gemini/test-model",
			SourceCheckAvailable: true, IndexPublishedAt: "2026-08-11T02:00:00Z",
		},
		live: index.NewLive(&index.Index{Version: "index1234567"}, "test"), state: shared, startedAt: time.Now().UTC(),
	}
	if err := shared.SaveSourceCheck(context.Background(), state.SourceCheck{
		CheckedAt: checkedAt, CheckedBy: "管理者", Changed: true,
		Deltas: []state.SourceDelta{{Source: "wiki", Updated: []string{"更新ページ"}}},
	}); err != nil {
		t.Fatal(err)
	}

	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, srv.testRequest(http.MethodGet, "/api/admin/overview", "", "管理者"))
	if res.Code != http.StatusOK {
		t.Fatalf("管理画面APIが失敗: %d %s", res.Code, res.Body.String())
	}
	for _, expected := range []string{`"stage":"verify_needed"`, `"changes":1`} {
		if !strings.Contains(res.Body.String(), expected) {
			t.Errorf("更新進捗に %s がない: %s", expected, res.Body.String())
		}
	}
}

func TestAdminDeletionIsAuditedButAuthorDeletionIsNot(t *testing.T) {
	srv, shared := testServer(t, []string{"管理者"})
	seedAssistant(t, shared, "admin-target", "作成者")
	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, srv.testRequest(http.MethodDelete, "/api/assistants/admin-target", "", "管理者"))
	if res.Code != http.StatusNoContent {
		t.Fatalf("管理者削除が失敗: %d", res.Code)
	}
	audits, _ := shared.ListAdminAudits(context.Background(), 10)
	if len(audits) != 1 || audits[0].Action != "assistant.delete" || audits[0].Target != "admin-target" {
		t.Fatalf("管理者削除の監査ログが不正: %+v", audits)
	}
}

func TestOwnerCanGrantAndRevokeCoAdmin(t *testing.T) {
	srv, shared := testServer(t, []string{"主管理者"})
	ctx := context.Background()
	if err := shared.SaveUserProfile(ctx, srv.userKey("共同管理者候補"), "共同管理者候補", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	grant := httptest.NewRecorder()
	srv.Routes().ServeHTTP(grant, srv.testRequest(http.MethodPost, "/api/admin/roles", `{"username":"共同管理者候補","enabled":true}`, "主管理者"))
	if grant.Code != http.StatusOK {
		t.Fatalf("共同管理者を追加できない: %d %s", grant.Code, grant.Body.String())
	}
	overview := httptest.NewRecorder()
	srv.Routes().ServeHTTP(overview, srv.testRequest(http.MethodGet, "/api/admin/overview", "", "共同管理者候補"))
	if overview.Code != http.StatusOK || !strings.Contains(overview.Body.String(), `"role":"co_admin"`) {
		t.Fatalf("共同管理者が管理画面へ入れない: %d %s", overview.Code, overview.Body.String())
	}
	forbidden := httptest.NewRecorder()
	srv.Routes().ServeHTTP(forbidden, srv.testRequest(http.MethodPost, "/api/admin/roles", `{"username":"別の候補","enabled":true}`, "共同管理者候補"))
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("共同管理者が権限を再付与できた: %d", forbidden.Code)
	}

	revoke := httptest.NewRecorder()
	srv.Routes().ServeHTTP(revoke, srv.testRequest(http.MethodPost, "/api/admin/roles", `{"username":"共同管理者候補","enabled":false}`, "主管理者"))
	if revoke.Code != http.StatusOK {
		t.Fatalf("共同管理者を解除できない: %d %s", revoke.Code, revoke.Body.String())
	}
	denied := httptest.NewRecorder()
	srv.Routes().ServeHTTP(denied, srv.testRequest(http.MethodGet, "/api/admin/overview", "", "共同管理者候補"))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("解除後も管理画面へ入れる: %d", denied.Code)
	}
	audits, _ := shared.ListAdminAudits(ctx, 10)
	actions := map[string]bool{}
	for _, audit := range audits {
		actions[audit.Action] = true
	}
	if !actions["admin.role.grant"] || !actions["admin.role.revoke"] {
		t.Fatalf("権限変更の監査ログが不足: %+v", audits)
	}
}

func TestGrantingCoAdminDoesNotOverwriteRoleWhenReadFails(t *testing.T) {
	shared := state.NewMemory()
	srv, _ := testServer(t, []string{"主管理者"})
	srv.state = &adminRoleReadErrorStore{Memory: shared, err: errors.New("読込失敗")}
	ctx := context.Background()
	key := srv.userKey("共同管理者候補")
	if err := shared.SaveUserProfile(ctx, key, "共同管理者候補", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := shared.SaveAdminRole(ctx, key, state.AdminRole{
		Username: "共同管理者候補", Tools: []string{"drive"},
	}); err != nil {
		t.Fatal(err)
	}

	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, srv.testRequest(http.MethodPost, "/api/admin/roles", `{"username":"共同管理者候補","enabled":true}`, "主管理者"))
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("読込失敗を無視した: status=%d body=%s", res.Code, res.Body.String())
	}
	role, ok, err := shared.GetAdminRole(ctx, key)
	if err != nil || !ok || len(role.Tools) != 1 || role.Tools[0] != "drive" || role.Role != "" {
		t.Fatalf("既存の許可を書き換えた: ok=%v role=%+v err=%v", ok, role, err)
	}
}

func TestAdminCanRunSourceCheckAndPersistResult(t *testing.T) {
	srv, shared := testServer(t, []string{"管理者"})
	srv.cfg.SourceCheckAvailable = true
	srv.cfg.SourceCheck = func(context.Context) ([]state.SourceDelta, error) {
		return []state.SourceDelta{{Source: "wiki", Updated: []string{"更新ページ"}}}, nil
	}

	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, srv.testRequest(http.MethodPost, "/api/admin/source-check", "", "管理者"))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"changed":true`) {
		t.Fatalf("更新確認が失敗: %d %s", res.Code, res.Body.String())
	}
	check, ok, err := shared.LatestSourceCheck(context.Background())
	if err != nil || !ok || !check.Changed || check.CheckedBy != "管理者" {
		t.Fatalf("更新確認結果を保存できていない: ok=%v result=%+v err=%v", ok, check, err)
	}
}
