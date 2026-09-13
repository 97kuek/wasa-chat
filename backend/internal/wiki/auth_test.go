package wiki

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/joho/godotenv"
)

func TestLoginUsesClientLogin(t *testing.T) {
	var loginAction string
	auth := New("https://wiki.example/api.php")
	auth.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("フォームを読めない: %v", err)
		}
		switch r.Form.Get("action") {
		case "query":
			return testResponse(r, `{"query":{"tokens":{"logintoken":"token+\\"}}}`), nil
		case "clientlogin":
			loginAction = r.Form.Get("action")
			if r.Form.Get("username") != "WASA利用者" || r.Form.Get("password") != "wiki-password" {
				t.Fatal("利用者名またはパスワードがWikiへ正しく中継されていない")
			}
			return testResponse(r, `{"clientlogin":{"status":"PASS","username":"WASA利用者"}}`), nil
		default:
			t.Fatalf("想定外の認証経路: %s", r.Form.Get("action"))
		}
		return nil, errors.New("想定外の認証経路")
	})

	user, err := auth.Login(context.Background(), "WASA利用者", "wiki-password")
	if err != nil {
		t.Fatalf("ログインに失敗した: %v", err)
	}
	if user != "WASA利用者" || loginAction != "clientlogin" {
		t.Fatalf("通常のWiki認証に統一されていない: user=%q action=%q", user, loginAction)
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	auth := New("https://wiki.example/api.php")
	auth.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("フォームを読めない: %v", err)
		}
		if r.Form.Get("action") == "query" {
			return testResponse(r, `{"query":{"tokens":{"logintoken":"token"}}}`), nil
		}
		return testResponse(r, `{"clientlogin":{"status":"FAIL"}}`), nil
	})

	if _, err := auth.Login(context.Background(), "利用者", "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("誤ったパスワードが弾かれていない: err=%v", err)
	}
}

func TestLoginTreatsInvalidWikiResponseAsUnavailable(t *testing.T) {
	auth := New("https://wiki.example/api.php")
	auth.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return testResponse(req, `not-json`), nil
	})
	_, err := auth.Login(context.Background(), "利用者", "password")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("壊れたWiki応答を認証失敗として扱った: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func testResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     http.StatusText(http.StatusOK),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

// 実際のWikiに繋いで確認する。通常テストで非公開Wikiへ接続しないよう明示フラグを必須にする。
//
//	cd backend && LIVE_WIKI_TEST=1 go test ./internal/wiki -run '^TestLoginSuccess$'
func liveAuth(t *testing.T) *Authenticator {
	t.Helper()
	if os.Getenv("LIVE_WIKI_TEST") != "1" {
		t.Skip("LIVE_WIKI_TEST=1 が未指定のため実Wiki認証をスキップ")
	}
	if os.Getenv("WIKI_API") == "" {
		if err := godotenv.Load("../../../.env"); err != nil {
			t.Fatalf("リポジトリ直下の.envを読み込めない: %v", err)
		}
	}
	api := os.Getenv("WIKI_API")
	if api == "" || os.Getenv("WIKI_USER") == "" {
		t.Fatal("WIKI_API / WIKI_USER が未設定")
	}
	return New(api)
}

func TestLoginSuccess(t *testing.T) {
	a := liveAuth(t)
	user, err := a.Login(context.Background(),
		os.Getenv("WIKI_USER"), os.Getenv("WIKI_PASS"))
	if err != nil {
		t.Fatalf("ログインに失敗した: %v", err)
	}
	if user == "" {
		t.Fatal("利用者名が空。Cookieに載せる識別子が取れていない")
	}
	t.Logf("利用者名: %s", user)
}

func TestLoginWrongPassword(t *testing.T) {
	a := liveAuth(t)
	if _, err := a.Login(context.Background(),
		os.Getenv("WIKI_USER"), "definitely-not-the-password"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("誤ったパスワードが弾かれていない: err=%v", err)
	}
}
