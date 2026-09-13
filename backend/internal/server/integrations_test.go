package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/97kuek/wasa-chat/backend/internal/calendar"
	"github.com/97kuek/wasa-chat/backend/internal/pipeline"
)

// カレンダーAPIの代わりに、決めた予定を返すサーバーを立てる。
func calendarStub(t *testing.T, items string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"summary":"WASA予定表","items":[` + items + `]}`))
	}))
	t.Cleanup(server.Close)
	// **キャッシュも捨てる。** 5分持つので、前のテストの結果が残る
	calendar.ResetCacheForTest(server.URL)
	t.Cleanup(func() { calendar.ResetCacheForTest("") })
}

func integration(t *testing.T, found []Integration, id string) Integration {
	t.Helper()
	for _, item := range found {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("%s の状態が返っていない: %+v", id, found)
	return Integration{}
}

// ⚠️ **「予定が0件」と「読めない」を混ぜない。**
//
// 以前は0件も未接続として扱っており、正しく設定できているのに
// 「共有設定を確かめてください」と出た。設定した直後は範囲内に予定が無いことが
// 普通にあるので、**直したばかりの設定を疑わせる**表示になる。
func TestCalendarConnectedEvenWithNoEvents(t *testing.T) {
	calendarStub(t, "")
	srv := toolServer(t, false)
	srv.cfg.CalendarIDs = []string{"wasa@example.com"}

	found := srv.calendarIntegration(t.Context())
	if !found.Connected {
		t.Fatalf("読めているのに未接続にしている: %+v", found)
	}
	if found.NextStep != "" {
		t.Fatalf("直すことが無いのに手順を出している: %q", found.NextStep)
	}
	if !strings.Contains(found.Summary, "予定がありません") {
		t.Fatalf("0件であることを伝えていない: %q", found.Summary)
	}
}

func TestCalendarReportsEventsWhenPresent(t *testing.T) {
	calendarStub(t, `{"summary":"荷重試験","start":{"date":"2026-10-01"},"end":{"date":"2026-10-02"}}`)
	srv := toolServer(t, false)
	srv.cfg.CalendarIDs = []string{"wasa@example.com"}

	found := srv.calendarIntegration(t.Context())
	if !found.Connected || !strings.Contains(found.Summary, "1件") {
		t.Fatalf("予定を読めていることを伝えていない: %+v", found)
	}
	if len(found.Detail) != 1 || found.Detail[0] != "WASA予定表" {
		t.Fatalf("どの予定表かを出していない: %+v", found.Detail)
	}
}

// 未設定のときは、次にやることを出す（環境変数を読める人しか分からない状態にしない）
func TestCalendarNotConfigured(t *testing.T) {
	srv := toolServer(t, false)
	found := srv.calendarIntegration(t.Context())
	if found.Connected || !strings.Contains(found.NextStep, "CALENDAR_IDS") {
		t.Fatalf("未設定の案内が出ていない: %+v", found)
	}
}

// ⚠️ **管理者しか見られない。** 連携状態にはサービスアカウントのアドレスや
// 参加しているDiscordサーバー名が入る
func TestIntegrationsNeedAdmin(t *testing.T) {
	srv := toolServer(t, false)
	res := httptest.NewRecorder()
	req := srv.testRequest(http.MethodGet, "/api/admin/integrations", "", "ただの部員")
	srv.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("管理者以外へ連携状態を返した: status=%d body=%s", res.Code, res.Body.String())
	}
}

// 招待URLは必要な権限だけを求める。管理者権限は求めない
func TestDiscordInviteAsksOnlyForWhatItUses(t *testing.T) {
	const viewChannel, sendMessages, readHistory = 1 << 10, 1 << 11, 1 << 16
	url := discordInviteURL("1548363233843355659")
	if !strings.Contains(url, "permissions=68608") {
		t.Fatalf("求める権限が変わっている: %s", url)
	}
	if discordInvitePermissions != viewChannel|sendMessages|readHistory {
		t.Fatalf("権限ビットが意図と違う: %d", discordInvitePermissions)
	}
	// アプリIDが無ければURLを作らない（押すと必ず失敗する導線を出さない）
	if discordInviteURL("") != "" {
		t.Fatal("アプリID無しで招待URLを作っている")
	}
}

// 共有ドライブは、索引に資料が入っていて初めて「つながっている」
func TestDriveIntegrationFollowsTheIndex(t *testing.T) {
	empty := toolServer(t, false).driveIntegration()
	if empty.Connected || !strings.Contains(empty.NextStep, "DRIVE_FOLDER_IDS") {
		t.Fatalf("未取り込みの案内が出ていない: %+v", empty)
	}
	// 共有相手に足すアドレスは、つながっていなくても出す（それが次の手順なので）
	srv := toolServer(t, false)
	srv.cfg.DriveServiceAccount = "wasa-chat-updater@example.iam.gserviceaccount.com"
	if got := srv.driveIntegration().ShareWith; got != srv.cfg.DriveServiceAccount {
		t.Fatalf("共有相手のアドレスを出していない: %q", got)
	}
}

// 管理画面は3つとも返す。増えたものが黙って落ちないようにする
func TestIntegrationsListsEveryService(t *testing.T) {
	srv := toolServer(t, false)
	res := httptest.NewRecorder()
	req := srv.testRequest(http.MethodGet, "/api/admin/integrations", "", "主管理者")
	srv.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("管理者が見られない: status=%d body=%s", res.Code, res.Body.String())
	}
	var found []Integration
	if err := json.Unmarshal(res.Body.Bytes(), &found); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{pipeline.ToolDiscord, pipeline.ToolDrive, pipeline.ToolCalendar} {
		integration(t, found, id)
	}
}
