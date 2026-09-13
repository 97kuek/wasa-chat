package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/97kuek/wasa-chat/backend/internal/state"
)

func testServer(t *testing.T, admins []string) (*Server, *state.Memory) {
	t.Helper()
	shared := state.NewMemory()
	return &Server{
		cfg:   Config{SessionSecret: "テスト用の固定鍵テスト用の固定鍵", DailyLimit: 30, AdminUsers: admins},
		state: shared,
	}, shared
}

// ログイン済みのリクエストを作る。Cookieの署名は本物を使う。
func (s *Server) testRequest(method, path, body, user string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.AddCookie(appCookie(cookieName, s.sign(user, time.Now().Add(time.Hour).Unix()), 3600))
	return req
}

func seedAssistant(t *testing.T, shared *state.Memory, id, author string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	err := shared.SaveAssistant(context.Background(), state.Assistant{
		ID: id, Name: id, Instruction: "語尾を〜だよにする",
		Author: author, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("下準備に失敗: %v", err)
	}
}

// 削除できるのは作成者本人と管理者だけ、というのがこの機能の唯一の権限規則。
func TestDeleteAssistantPermission(t *testing.T) {
	cases := []struct {
		name string
		user string
		want int
	}{
		{"作成者本人", "41 Hanako", http.StatusNoContent},
		{"管理者", "42 Wasa Taro", http.StatusNoContent},
		{"第三者", "43 Taro", http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, shared := testServer(t, []string{"42 Wasa Taro"})
			seedAssistant(t, shared, "target", "41 Hanako")

			res := httptest.NewRecorder()
			srv.Routes().ServeHTTP(res, srv.testRequest("DELETE", "/api/assistants/target", "", c.user))
			if res.Code != c.want {
				t.Fatalf("状態コード = %d, want %d（本文: %s）", res.Code, c.want, res.Body)
			}

			list, _ := shared.ListAssistants(context.Background())
			deleted := len(list) == 0
			if deleted != (c.want == http.StatusNoContent) {
				t.Errorf("削除の結果が権限と一致しない: 残り%d件", len(list))
			}
		})
	}
}

// 作成者は本文ではなくCookieから取る。ここを本文から取ると他人の名前で
// アシスタントを作れてしまい、名前が出ることによる抑止が消える。
func TestCreateAssistantIgnoresAuthorInBody(t *testing.T) {
	srv, shared := testServer(t, nil)
	body := `{"id":"fake","name":"なりすまし","instruction":"語尾を〜だよにする","author":"42 Wasa Taro"}`

	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, srv.testRequest("POST", "/api/assistants", body, "43 Taro"))
	if res.Code != http.StatusCreated {
		t.Fatalf("作成に失敗: %d %s", res.Code, res.Body)
	}
	list, _ := shared.ListAssistants(context.Background())
	if len(list) != 1 || list[0].Author != "43 Taro" {
		t.Fatalf("作成者が本文の値で上書きされた: %+v", list)
	}
}

// 同じIDでの上書きを許すと、他人のアシスタントを実質的に編集できてしまう。
func TestCreateAssistantRejectsDuplicateID(t *testing.T) {
	srv, shared := testServer(t, nil)
	seedAssistant(t, shared, "taken", "41 Hanako")

	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, srv.testRequest("POST", "/api/assistants",
		`{"id":"taken","name":"横取り","instruction":"上書きしたい"}`, "43 Taro"))
	if res.Code != http.StatusConflict {
		t.Fatalf("重複IDが通った: %d %s", res.Code, res.Body)
	}
	list, _ := shared.ListAssistants(context.Background())
	if list[0].Author != "41 Hanako" {
		t.Error("既存のアシスタントが別人に奪われた")
	}
}

// 削除の可否はサーバーが判定して配る。画面側で作成者名を突き合わせる実装だと
// 判定が2箇所に散り、片方だけ直したときに食い違う。
func TestListAssistantsReportsPermission(t *testing.T) {
	srv, shared := testServer(t, []string{"42 Wasa Taro"})
	seedAssistant(t, shared, "mine", "43 Taro")
	seedAssistant(t, shared, "theirs", "41 Hanako")

	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, srv.testRequest("GET", "/api/assistants", "", "43 Taro"))
	if res.Code != http.StatusOK {
		t.Fatalf("一覧の取得に失敗: %d", res.Code)
	}
	var body struct {
		Assistants []struct {
			ID      string `json:"id"`
			Author  string `json:"author"`
			CanEdit bool   `json:"canEdit"`
		} `json:"assistants"`
		Teams []struct {
			Value string `json:"value"`
			Label string `json:"label"`
		} `json:"teams"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("応答を解析できない: %v", err)
	}
	if len(body.Assistants) != 2 || len(body.Teams) == 0 {
		t.Fatalf("一覧の中身が足りない: %+v", body)
	}
	for _, item := range body.Assistants {
		// 作成者名は隠さない。匿名で共有できないことが抑止になっている
		if item.Author == "" {
			t.Error("作成者名が空で返っている")
		}
		if want := item.ID == "mine"; item.CanEdit != want {
			t.Errorf("%s の編集可否 = %v, want %v", item.ID, item.CanEdit, want)
		}
	}
}

func TestAssistantRoutesRequireAuth(t *testing.T) {
	srv, _ := testServer(t, nil)
	for _, path := range []string{"GET /api/assistants", "POST /api/assistants", "DELETE /api/assistants/x"} {
		method, target, _ := strings.Cut(path, " ")
		res := httptest.NewRecorder()
		srv.Routes().ServeHTTP(res, httptest.NewRequest(method, target, strings.NewReader("{}")))
		if res.Code != http.StatusUnauthorized {
			t.Errorf("%s が未ログインで通った: %d", path, res.Code)
		}
	}
}

// 解決できないアシスタントで質問されたとき、汎用へ落として続行しない。
// 参照範囲を絞ったつもりの質問が全資料参照へ黙って広がるのを防ぐ。
func TestAskRejectsUnresolvableAssistant(t *testing.T) {
	srv, _ := testServer(t, nil)
	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, srv.testRequest("POST", "/api/ask",
		`{"question":"鳥コンの流れは","assistantId":"消えたやつ"}`, "43 Taro"))
	if res.Code != http.StatusConflict {
		t.Fatalf("存在しないアシスタントで質問が通った: %d %s", res.Code, res.Body)
	}
}

// 同じIDの同時作成で、後から来た方が他人のアシスタントを奪えないこと。
// 一覧で重複を調べてから書く方式だと、両方が検査を通って後勝ちになる。
func TestCreateAssistantConcurrentSameID(t *testing.T) {
	srv, shared := testServer(t, nil)
	const users = 8

	var wg sync.WaitGroup
	codes := make([]int, users)
	start := make(chan struct{})
	for i := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := fmt.Sprintf(`{"id":"same","name":"%d番","instruction":"語尾を〜だよにする"}`, i)
			res := httptest.NewRecorder()
			<-start
			srv.Routes().ServeHTTP(res, srv.testRequest("POST", "/api/assistants", body, fmt.Sprintf("4%d Taro", i)))
			codes[i] = res.Code
		}()
	}
	close(start)
	wg.Wait()

	created := 0
	for _, code := range codes {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Errorf("想定外の状態コード: %d", code)
		}
	}
	if created != 1 {
		t.Errorf("同じIDで%d件成功した（1件であるべき）", created)
	}
	list, _ := shared.ListAssistants(context.Background())
	if len(list) != 1 {
		t.Errorf("保存されたのが%d件", len(list))
	}
}

// 編集できるのは作成者本人と管理者だけ。他人のものは複製して直す原則を守る。
func TestUpdateAssistantPermission(t *testing.T) {
	cases := []struct {
		name string
		user string
		want int
	}{
		{"作成者本人", "41 Hanako", http.StatusOK},
		{"管理者", "42 Wasa Taro", http.StatusOK},
		{"第三者", "43 Taro", http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, shared := testServer(t, []string{"42 Wasa Taro"})
			seedAssistant(t, shared, "target", "41 Hanako")

			res := httptest.NewRecorder()
			srv.Routes().ServeHTTP(res, srv.testRequest("PUT", "/api/assistants/target",
				`{"name":"直した名前","instruction":"語尾を〜っすにする"}`, c.user))
			if res.Code != c.want {
				t.Fatalf("状態コード = %d, want %d（本文: %s）", res.Code, c.want, res.Body)
			}

			list, _ := shared.ListAssistants(context.Background())
			changed := list[0].Name == "直した名前"
			if changed != (c.want == http.StatusOK) {
				t.Errorf("編集の結果が権限と一致しない: %+v", list[0])
			}
		})
	}
}

// IDと作成者は編集で変えられない。IDが変わると選択中の設定が黙って外れ、
// 作成者が変わると「誰が作ったか」を出すことによる抑止が消える。
func TestUpdateAssistantKeepsIdentity(t *testing.T) {
	srv, shared := testServer(t, nil)
	seedAssistant(t, shared, "mine", "43 Taro")

	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, srv.testRequest("PUT", "/api/assistants/mine",
		`{"id":"stolen","name":"名前","instruction":"指示","author":"41 Hanako"}`, "43 Taro"))
	if res.Code != http.StatusOK {
		t.Fatalf("編集に失敗: %d %s", res.Code, res.Body)
	}
	list, _ := shared.ListAssistants(context.Background())
	if len(list) != 1 || list[0].ID != "mine" || list[0].Author != "43 Taro" {
		t.Fatalf("IDか作成者が書き換わった: %+v", list)
	}
}

func TestUpdateAssistantUpdatesGlossary(t *testing.T) {
	srv, shared := testServer(t, nil)
	seedAssistant(t, shared, "mine", "43 Taro")

	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, srv.testRequest(http.MethodPut, "/api/assistants/mine",
		`{"name":"名前","instruction":"指示","glossary":[{"term":"TF","meaning":"テストフライト"}]}`, "43 Taro"))
	if res.Code != http.StatusOK {
		t.Fatalf("編集に失敗: %d %s", res.Code, res.Body)
	}
	list, err := shared.ListAssistants(context.Background())
	if err != nil || len(list) != 1 || len(list[0].Glossary) != 1 || list[0].Glossary[0].Term != "TF" {
		t.Fatalf("用語集を更新できていない: list=%+v err=%v", list, err)
	}
}
