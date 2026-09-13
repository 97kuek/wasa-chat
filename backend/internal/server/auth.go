package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/97kuek/wasa-chat/backend/internal/wiki"
)

// Wikiの通常アカウントで本人確認する。共有パスワード1本だと全員が同じ利用者に
// なってしまい、レート制限を個人ごとに分けられないため。
// パスワードは検証に使って捨て、Cookieには利用者名と有効期限だけを載せる。

func (s *Server) sign(user string, expiry int64) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	fmt.Fprintf(mac, "%s|%d", user, expiry)
	return fmt.Sprintf("%s|%d.%s", base64.RawURLEncoding.EncodeToString([]byte(user)),
		expiry, base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}

// verify はCookieを検証し、利用者名を返す。
func (s *Server) verify(token string) (string, bool) {
	body, _, ok := strings.Cut(token, ".")
	if !ok {
		return "", false
	}
	encoded, rawExpiry, ok := strings.Cut(body, "|")
	if !ok {
		return "", false
	}
	user, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", false
	}
	expiry, err := strconv.ParseInt(rawExpiry, 10, 64)
	if err != nil || time.Now().Unix() > expiry {
		return "", false
	}
	if !hmac.Equal([]byte(s.sign(string(user), expiry)), []byte(token)) {
		return "", false
	}
	return string(user), true
}

// Firestoreのパスに利用者名を残さない。固定鍵を使うことで、別端末でも同じ
// Wiki利用者を同じ保存先へ結び付けられる。
func (s *Server) userKey(user string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	fmt.Fprintf(mac, "state|%s", user)
	return hex.EncodeToString(mac.Sum(nil))
}

func appCookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode,
		// Cloudflare PagesとCloud Runが別サイトでも、対応ブラウザでは
		// Cookieをトップレベルサイト単位に分離してログインを維持できる。
		Partitioned: true,
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, maxSmallRequestBodyBytes, &body); err != nil {
		writeJSON(w, invalidJSONStatus(err), map[string]string{"error": "リクエストが不正です"})
		return
	}

	// パスワードはここで使い切る。保存もログ出力もしない
	user, err := s.auth.Login(r.Context(), strings.TrimSpace(body.Username), body.Password)
	if err != nil {
		time.Sleep(loginFailureDelay) // 総当たりの速度を落とす
		status, message := http.StatusUnauthorized, err.Error()
		if errors.Is(err, wiki.ErrUnavailable) {
			status = http.StatusBadGateway
			log.Printf("Wikiへの接続に失敗: %v", err)
			message = "Wikiに接続できませんでした。しばらくしてからお試しください"
		}
		writeJSON(w, status, map[string]string{"error": message})
		return
	}
	// 実名と利用回数を管理画面で結び付けるための最小限のプロフィール。
	// 質問・回答はここへ保存せず、失敗しても認証自体は妨げない。
	now := time.Now().UTC()
	if err := s.state.SaveUserProfile(r.Context(), s.userKey(user), user, now); err != nil {
		log.Printf("利用者プロフィールの保存に失敗: %v", err)
	}
	if s.isAdmin(r.Context(), user) {
		s.saveAdminAudit(r.Context(), user, "admin.login", "")
	}

	http.SetCookie(w, appCookie(
		cookieName,
		s.sign(user, time.Now().Add(sessionMaxAge).Unix()),
		int(sessionMaxAge.Seconds()),
	))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "username": user})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	remaining := 0
	icon := ""
	if ok {
		// デプロイ前から有効なCookieを持つ利用者も、次に画面を開いた時点で
		// 管理用プロフィールへ載せる。質問・回答はここへ保存しない。
		if err := s.state.SaveUserProfile(r.Context(), s.userKey(user), user, time.Now().UTC()); err != nil {
			log.Printf("利用者プロフィールの更新に失敗: %v", err)
		}
		var err error
		remaining, err = s.state.Remaining(r.Context(), s.userKey(user), today(), s.cfg.DailyLimit)
		if err != nil {
			log.Printf("利用回数の読み込みに失敗: %v", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "利用回数を読み込めませんでした"})
			return
		}
		profile, exists, err := s.state.GetUserProfile(r.Context(), s.userKey(user))
		if err != nil {
			// 画像は見た目だけの情報なので、読めなくてもログインと質問は止めない。
			log.Printf("利用者画像の読み込みに失敗: %v", err)
		} else if exists {
			icon = profile.Icon
		}
	}
	writeJSON(w, http.StatusOK,
		map[string]any{"authenticated": ok, "username": user, "icon": icon, "remaining": remaining, "admin": ok && s.isAdmin(r.Context(), user)})
}

func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, appCookie(cookieName, "", -1))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) currentUser(r *http.Request) (string, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", false
	}
	return s.verify(c.Value)
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.currentUser(r); !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "ログインしてください"})
			return
		}
		next(w, r)
	}
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := s.currentUser(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "ログインしてください"})
			return
		}
		role, err := s.adminRole(r.Context(), user)
		if err != nil {
			// 保存先の障害を権限不足に見せると、共同管理者が復旧方法を判断できない。
			log.Printf("管理者ロールの読み込みに失敗: %v", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "管理者権限を確認できませんでした"})
			return
		}
		if role == "" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "管理者だけが利用できます"})
			return
		}
		next(w, r)
	}
}

func (s *Server) isOwner(user string) bool {
	for _, admin := range s.cfg.AdminUsers {
		if user == admin {
			return true
		}
	}
	return false
}

func (s *Server) adminRole(ctx context.Context, user string) (string, error) {
	if s.isOwner(user) {
		return "owner", nil
	}
	role, ok, err := s.state.GetAdminRole(ctx, s.userKey(user))
	if err != nil {
		return "", err
	}
	if ok && role.Role == "co_admin" && role.Username == user {
		return role.Role, nil
	}
	return "", nil
}

func (s *Server) isAdmin(ctx context.Context, user string) bool {
	if user == "" {
		return false
	}
	role, err := s.adminRole(ctx, user)
	if err != nil {
		log.Printf("管理者ロールの読み込みに失敗: %v", err)
		return false
	}
	return role != ""
}

func (s *Server) requireOwner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := s.currentUser(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "ログインしてください"})
			return
		}
		if !s.isOwner(user) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "主管理者だけが共同管理者を変更できます"})
			return
		}
		next(w, r)
	}
}
