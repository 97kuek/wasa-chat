package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProfileIconIsSavedForLoggedInUserAndReturnedBySession(t *testing.T) {
	srv, shared := testServer(t, nil)
	const user = "43 Taro"
	const icon = "data:image/png;base64,AA=="

	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, srv.testRequest("PUT", "/api/profile/icon", `{"icon":"`+icon+`"}`, user))
	if res.Code != http.StatusOK {
		t.Fatalf("画像を保存できない: %d %s", res.Code, res.Body)
	}
	profile, ok, err := shared.GetUserProfile(context.Background(), srv.userKey(user))
	if err != nil || !ok || profile.Icon != icon {
		t.Fatalf("本人のプロフィールへ保存されていない: ok=%v err=%v profile=%+v", ok, err, profile)
	}

	sessionRes := httptest.NewRecorder()
	srv.Routes().ServeHTTP(sessionRes, srv.testRequest("GET", "/api/session", "", user))
	var sessionBody struct {
		Icon string `json:"icon"`
	}
	if err := json.Unmarshal(sessionRes.Body.Bytes(), &sessionBody); err != nil || sessionBody.Icon != icon {
		t.Fatalf("セッションで画像を読み戻せない: err=%v body=%s", err, sessionRes.Body)
	}
}

func TestProfileIconCanBeRemoved(t *testing.T) {
	srv, shared := testServer(t, nil)
	const user = "43 Taro"
	for _, icon := range []string{"data:image/png;base64,AA==", ""} {
		res := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]string{"icon": icon})
		srv.Routes().ServeHTTP(res, srv.testRequest("PUT", "/api/profile/icon", string(body), user))
		if res.Code != http.StatusOK {
			t.Fatalf("画像の更新に失敗: %d %s", res.Code, res.Body)
		}
	}
	profile, _, _ := shared.GetUserProfile(context.Background(), srv.userKey(user))
	if profile.Icon != "" {
		t.Fatal("利用者画像を外せていない")
	}
}

func TestProfileIconRejectsUnsafeImageAndRequiresLogin(t *testing.T) {
	srv, _ := testServer(t, nil)

	invalid := httptest.NewRecorder()
	srv.Routes().ServeHTTP(invalid, srv.testRequest("PUT", "/api/profile/icon", `{"icon":"data:image/svg+xml;base64,AA=="}`, "43 Taro"))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("SVGを受け付けた: %d %s", invalid.Code, invalid.Body)
	}

	unauthorized := httptest.NewRecorder()
	srv.Routes().ServeHTTP(unauthorized, httptest.NewRequest("PUT", "/api/profile/icon", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("未ログインで更新できた: %d", unauthorized.Code)
	}
}
