// Package server はHTTP層。認証・レート制限・SSEストリーミングを担う。
package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/97kuek/wasa-chat/backend/internal/assistant"
	"github.com/97kuek/wasa-chat/backend/internal/index"
	"github.com/97kuek/wasa-chat/backend/internal/llm"
	"github.com/97kuek/wasa-chat/backend/internal/pipeline"
	"github.com/97kuek/wasa-chat/backend/internal/state"
	"github.com/97kuek/wasa-chat/backend/internal/wiki"
)

type Config struct {
	SessionSecret    string // Cookie署名用。未設定なら起動時に生成する
	DailyLimit       int    // 利用者1人あたりの1日の質問数上限
	APIDailyLimit    int    // Geminiのモデル別RPD。実送信回数から残量を推定する
	AllowOrigin      string // 開発時にViteのdev serverから叩くためのCORS設定
	SPADir           string // 指定するとビルド済みSPAも同じサーバーから配る
	StoreName        string // 管理画面へ出す保存先名（秘密値は含めない）
	IndexSource      string // 管理画面へ出す索引の読込元
	Revision         string // Cloud RunのK_REVISION
	CodeVersion      string // デプロイしたGitコミット。K_REVISIONとは別にコードを照合する
	IndexPublishedAt string // publish-index.shが索引差し替え時に記録するRFC3339日時
	LLMName          string // APIキーを含まないプロバイダ・モデル名
	LLMStatus        func() llm.RuntimeStatus
	// SourceCheck は現在の索引とWiki・公式サイトを読み取り専用で照合する。
	SourceCheck          func(context.Context) ([]state.SourceDelta, error)
	SourceCheckAvailable bool
	// AdminUsersは画面から外せない主管理者。共同管理者はFirestoreへ保存する。
	// 設定を復旧口に残し、画面操作だけで管理者がゼロになる事故を防ぐ。
	AdminUsers []string
}

// spaHandler は静的ファイルを返し、見つからないパスは index.html に落とす
// （クライアント側ルーティングのため）。
func spaHandler(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(filepath.Join(dir, filepath.Clean(r.URL.Path))); err != nil {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
}

type Server struct {
	cfg       Config
	live      *index.Live
	pipe      *pipeline.Pipeline
	auth      *wiki.Authenticator
	state     state.Store
	startedAt time.Time
	sourceMu  sync.Mutex
}

func New(cfg Config, live *index.Live, pipe *pipeline.Pipeline, auth *wiki.Authenticator, shared state.Store) *Server {
	return &Server{cfg: cfg, live: live, pipe: pipe, auth: auth, state: shared, startedAt: time.Now().UTC()}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("PUT /api/profile/icon", s.requireAuth(s.handleUpdateProfileIcon))
	mux.HandleFunc("POST /api/ask", s.requireAuth(s.handleAsk))
	mux.HandleFunc("GET /api/chats", s.requireAuth(s.handleListChats))
	mux.HandleFunc("PUT /api/chats/{id}", s.requireAuth(s.handleSaveChat))
	mux.HandleFunc("DELETE /api/chats/{id}", s.requireAuth(s.handleDeleteChat))
	mux.HandleFunc("POST /api/feedback", s.requireAuth(s.handleSaveFeedback))
	mux.HandleFunc("GET /api/admin/overview", s.requireAdmin(s.handleAdminOverview))
	mux.HandleFunc("POST /api/admin/roles", s.requireOwner(s.handleAdminRole))
	mux.HandleFunc("POST /api/admin/source-check", s.requireAdmin(s.handleSourceCheck))
	mux.HandleFunc("GET /api/assistants", s.requireAuth(s.handleListAssistants))
	mux.HandleFunc("POST /api/assistants", s.requireAuth(s.handleCreateAssistant))
	mux.HandleFunc("PUT /api/assistants/{id}", s.requireAuth(s.handleUpdateAssistant))
	mux.HandleFunc("DELETE /api/assistants/{id}", s.requireAuth(s.handleDeleteAssistant))
	// Cloud Runでは末尾が z の一部パスがプラットフォーム側で処理され、
	// アプリまで届かず404になるため、予約されない /health を使う。
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		pages, chunks := s.live.Current().Stats()
		status := s.live.Status()
		// **読み直しの失敗を隠さない。** 起動時は索引が読めなければ止めるのに、
		// 起動後は黙って古いまま動き続ける、という非対称を残さない（docs/09 B-2）。
		// 外形監視はここを見れば「古い索引で答え続けている」に気づける
		code := http.StatusOK
		if status.Failures > 0 {
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, map[string]any{
			"ok": status.Failures == 0, "pages": pages, "chunks": chunks, "index": status,
		})
	})

	// SPAを同梱して配る場合（SPA_DIR 指定時）。フロントを Cloudflare Pages に
	// 置くなら不要だが、Docker一つで動かしたいときにこちらが使える。
	if s.cfg.SPADir != "" {
		mux.Handle("GET /", spaHandler(s.cfg.SPADir))
	}
	return s.withCORS(mux)
}

// ---------------------------------------------------------------- 認証
//
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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSmallRequestBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "リクエストが不正です"})
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
		if !s.isAdmin(r.Context(), user) {
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

// ---------------------------------------------------------------- チャット履歴

func (s *Server) handleListChats(w http.ResponseWriter, r *http.Request) {
	user, _ := s.currentUser(r)
	chats, err := s.state.ListChats(r.Context(), s.userKey(user), maxChats)
	if err != nil {
		log.Printf("チャット履歴の読み込みに失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "チャット履歴を読み込めませんでした"})
		return
	}
	// 保存済みの古い履歴には nil が残っているため、読み出し側でも均す
	for i, chat := range chats {
		chats[i] = state.NormalizeChat(chat)
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": chats})
}

func validChatID(id string) bool {
	if id == "" || len(id) > maxChatIDBytes {
		return false
	}
	for _, char := range id {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func validTime(value string) bool {
	parsed, err := time.Parse(time.RFC3339, value)
	return err == nil && parsed.Before(time.Now().Add(maxFutureClockSkew))
}

func validateChat(chat *state.Chat, expectedID string) bool {
	if chat.ID != expectedID || !validChatID(chat.ID) ||
		strings.TrimSpace(chat.Title) == "" || len([]rune(chat.Title)) > maxChatTitleRunes ||
		!validTime(chat.CreatedAt) || !validTime(chat.UpdatedAt) ||
		len(chat.Turns) == 0 || len(chat.Turns) > maxTurnsPerChat {
		return false
	}
	for i := range chat.Turns {
		turn := &chat.Turns[i]
		if strings.TrimSpace(turn.Question) == "" || len([]rune(turn.Question)) > maxQuestionRunes ||
			len([]rune(turn.Answer)) > maxAnswerRunes || len([]rune(turn.Error)) > maxTurnErrorRunes ||
			len(turn.Sources) > maxSourcesPerTurn ||
			// 画像を履歴へ入れさせない。1チャットで上限を超える
			len(turn.AssistantID) > maxAssistantIDBytes || len([]rune(turn.AssistantName)) > maxAssistantNameRunes {
			return false
		}
		if _, ok := pipeline.ParseResponseMode(turn.ResponseMode); !ok {
			return false
		}
		if turn.ResolvedMode != "" {
			resolved, ok := pipeline.ParseResponseMode(turn.ResolvedMode)
			if !ok || resolved == pipeline.ModeAuto {
				return false
			}
		}
		if !validStageTimings(turn.Timings) {
			return false
		}
		if turn.FeedbackRating != "" && turn.FeedbackRating != "good" && turn.FeedbackRating != "bad" ||
			turn.FeedbackRating == "" && (len(turn.FeedbackReasons) > 0 || turn.FeedbackComment != "") ||
			len(turn.FeedbackReasons) > maxFeedbackReasons || len([]rune(turn.FeedbackComment)) > maxFeedbackCommentRunes ||
			!validFeedbackReasons(turn.FeedbackReasons) ||
			!feedbackReasonsMatch("answer", turn.FeedbackRating, turn.FeedbackReasons) {
			return false
		}
		for _, source := range turn.Sources {
			parsed, err := url.Parse(source.URL)
			if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" ||
				len([]rune(source.Title)) > maxSourceTitleRunes || len(source.URL) > maxSourceURLBytes {
				return false
			}
		}
		// 中断中の状態は端末間で復元しても再開できないため、完了済みとして保存する。
		turn.Status = ""
		turn.Streaming = false
	}
	return true
}

func (s *Server) handleSaveChat(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("id")
	var chat state.Chat
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChatBodyBytes)).Decode(&chat); err != nil ||
		!validateChat(&chat, chatID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "チャット履歴が不正です"})
		return
	}
	user, _ := s.currentUser(r)
	if err := s.state.SaveChat(r.Context(), s.userKey(user), state.NormalizeChat(chat), maxChats); err != nil {
		log.Printf("チャット履歴の保存に失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "チャット履歴を保存できませんでした"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteChat(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("id")
	if !validChatID(chatID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "チャットIDが不正です"})
		return
	}
	user, _ := s.currentUser(r)
	if err := s.state.DeleteChat(r.Context(), s.userKey(user), chatID); err != nil {
		log.Printf("チャット履歴の削除に失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "チャット履歴を削除できませんでした"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- フィードバック

func validStageTimings(timings *state.StageTimings) bool {
	if timings == nil {
		return true
	}
	values := []int64{timings.PagesMS, timings.ChunksMS, timings.AnswerMS, timings.TotalMS}
	for _, value := range values {
		if value < 0 || value > maxStageTimingMS {
			return false
		}
	}
	if timings.TotalMS > 0 &&
		(timings.PagesMS > timings.TotalMS || timings.ChunksMS > timings.TotalMS || timings.AnswerMS > timings.TotalMS) {
		return false
	}
	return true
}

var feedbackReasons = map[string]bool{
	"helpful": true, "clear": true, "good_sources": true,
	"incorrect": true, "missing": true, "unclear": true, "wrong_sources": true,
	"outdated": true, "slow": true,
	"bug": true, "usability": true, "feature": true, "content": true, "other": true,
}

var goodFeedbackReasons = map[string]bool{"helpful": true, "clear": true, "good_sources": true}
var badFeedbackReasons = map[string]bool{
	"incorrect": true, "missing": true, "unclear": true, "wrong_sources": true,
	"outdated": true, "slow": true,
}
var generalFeedbackReasons = map[string]bool{
	"bug": true, "usability": true, "feature": true, "content": true, "other": true,
}

func validFeedbackReasons(reasons []string) bool {
	seen := map[string]bool{}
	for _, reason := range reasons {
		if !feedbackReasons[reason] || seen[reason] {
			return false
		}
		seen[reason] = true
	}
	return true
}

func feedbackReasonsMatch(kind, rating string, reasons []string) bool {
	allowed := generalFeedbackReasons
	if kind == "answer" && rating == "good" {
		allowed = goodFeedbackReasons
	} else if kind == "answer" && rating == "bad" {
		allowed = badFeedbackReasons
	} else if kind == "answer" && rating == "" {
		return len(reasons) == 0
	}
	for _, reason := range reasons {
		if !allowed[reason] {
			return false
		}
	}
	return true
}

func validFeedbackClientID(id string) bool {
	if id == "" || len(id) > maxFeedbackClientIDBytes {
		return false
	}
	for _, char := range id {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '-' || char == '_' || char == ':' {
			continue
		}
		return false
	}
	return true
}

func feedbackSourcesValid(sources []state.Source) bool {
	if len(sources) > maxFeedbackSources {
		return false
	}
	for _, source := range sources {
		parsed, err := url.Parse(source.URL)
		if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" ||
			len([]rune(source.Title)) > maxFeedbackSourceTitle || len(source.URL) > maxFeedbackSourceURL {
			return false
		}
	}
	return true
}

func (s *Server) feedbackID(userKey, clientID string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	fmt.Fprintf(mac, "feedback|%s|%s", userKey, clientID)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) handleSaveFeedback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientID      string              `json:"clientId"`
		Kind          string              `json:"kind"`
		Rating        string              `json:"rating"`
		Reasons       []string            `json:"reasons"`
		Comment       string              `json:"comment"`
		Question      string              `json:"question"`
		Answer        string              `json:"answer"`
		Sources       []state.Source      `json:"sources"`
		AssistantID   string              `json:"assistantId"`
		AssistantName string              `json:"assistantName"`
		ResponseMode  string              `json:"responseMode"`
		ResolvedMode  string              `json:"resolvedMode"`
		Timings       *state.StageTimings `json:"timings"`
		ChatID        string              `json:"chatId"`
		TurnIndex     int                 `json:"turnIndex"`
		Page          string              `json:"page"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxFeedbackBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "フィードバックが不正です"})
		return
	}
	body.Comment = strings.TrimSpace(body.Comment)
	validKind := body.Kind == "answer" || body.Kind == "general"
	validRating := body.Rating == "" || body.Rating == "good" || body.Rating == "bad"
	if !validFeedbackClientID(body.ClientID) || !validKind || !validRating ||
		len(body.Reasons) > maxFeedbackReasons || !validFeedbackReasons(body.Reasons) ||
		!feedbackReasonsMatch(body.Kind, body.Rating, body.Reasons) ||
		len([]rune(body.Comment)) > maxFeedbackCommentRunes || len([]rune(body.Question)) > maxFeedbackQuestionRunes ||
		len([]rune(body.Answer)) > maxFeedbackAnswerRunes || !feedbackSourcesValid(body.Sources) ||
		len(body.AssistantID) > maxFeedbackAssistantID || len([]rune(body.AssistantName)) > maxFeedbackAssistantRunes ||
		!validStageTimings(body.Timings) ||
		len(body.ChatID) > maxFeedbackChatID || body.TurnIndex < 0 || body.TurnIndex > maxFeedbackTurnIndex ||
		(body.Page != "chat" && body.Page != "assistants") ||
		(body.Kind == "answer" && (body.Rating == "" || body.ChatID == "" || strings.TrimSpace(body.Question) == "")) ||
		(body.Kind == "general" && (body.Rating != "" || body.Timings != nil || len(body.Reasons) == 0 && body.Comment == "")) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "フィードバックが不正です"})
		return
	}
	if _, ok := pipeline.ParseResponseMode(body.ResponseMode); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "回答モードが不正です"})
		return
	}
	if body.ResolvedMode != "" {
		mode, ok := pipeline.ParseResponseMode(body.ResolvedMode)
		if !ok || mode == pipeline.ModeAuto {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "回答モードが不正です"})
			return
		}
	}
	user, _ := s.currentUser(r)
	userKey := s.userKey(user)
	item := state.Feedback{
		ID: s.feedbackID(userKey, body.ClientID), ReporterKey: userKey,
		Kind: body.Kind, Rating: body.Rating, Reasons: body.Reasons, Comment: body.Comment,
		Question: body.Question, Answer: body.Answer, Sources: body.Sources,
		AssistantID: body.AssistantID, AssistantName: body.AssistantName,
		ResponseMode: body.ResponseMode, ResolvedMode: body.ResolvedMode, Timings: body.Timings,
		ChatID: body.ChatID, TurnIndex: body.TurnIndex, Page: body.Page,
		SubmittedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.state.SaveFeedback(r.Context(), item); err != nil {
		log.Printf("フィードバックの保存に失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "フィードバックを送信できませんでした"})
		return
	}
	// 保存のたびに保存期限を過ぎた報告を消す。定期実行の仕組みを別に持たない方針なので、
	// ここで消さないとプライバシーポリシーに書いた1年が守られない。
	// 削除に失敗しても保存済みの報告は正しいため、記録だけしてエラーにはしない。
	cutoff := time.Now().UTC().AddDate(-1, 0, 0).Format(time.RFC3339)
	if removed, err := s.state.PurgeFeedback(r.Context(), cutoff); err != nil {
		log.Printf("保存期限を過ぎたフィードバックの削除に失敗: %v", err)
	} else if removed > 0 {
		log.Printf("保存期限を過ぎたフィードバックを%d件削除した", removed)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- アシスタント
//
// 全員で共有する。審査はしない代わりに、悪い設定が危険ではなく退屈になるよう
// 構造で縛ってある（internal/assistant のパッケージコメントを参照）。

// assistantView は一覧に載せる形。権限の判定結果をサーバー側で付ける。
// 画面側で作成者名を突き合わせる実装にすると、判定が2箇所に散る。
type assistantView struct {
	state.Assistant
	Scope string `json:"scope"`
	// 編集と削除は同じ権限（作成者本人と管理者）。画面では別のボタンになるが、
	// 判定を2つに分けると片方だけ直したときに食い違う
	CanEdit bool `json:"canEdit"`
}

func (s *Server) assistantViews(ctx context.Context, list []state.Assistant, user string) []assistantView {
	isAdmin := s.isAdmin(ctx, user)
	views := make([]assistantView, 0, len(list))
	for _, item := range list {
		views = append(views, assistantView{
			Assistant: item,
			Scope:     assistant.ScopeLabel(&item),
			CanEdit:   assistant.CanEdit(item, user, isAdmin),
		})
	}
	return views
}

func (s *Server) handleListAssistants(w http.ResponseWriter, r *http.Request) {
	user, _ := s.currentUser(r)
	list, err := s.state.ListAssistants(r.Context())
	if err != nil {
		log.Printf("アシスタントの読み込みに失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "アシスタントを読み込めませんでした"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"assistants":  s.assistantViews(r.Context(), list, user),
		"teams":       assistant.Teams,
		"scopeCounts": s.scopeCounts(),
	})
}

// scopeCounts は、出所と区分の組み合わせごとに何ページ参照できるかを返す。
//
// 参照範囲の指定は今まで「選べるが、結果が見えない」ものだった。0件になる
// 組み合わせ（公式サイト × 電装班など）を選んでも、質問するまで気付けない。
// 索引を数えるだけなので、一覧を返すついでに全組み合わせを渡して画面で引く。
//
// 検索対象にならないページ（チャンクが無いもの）は数えない。目次には残るが
// 本文を読めないため、「参照できる資料の数」としては誤解になる。
func (s *Server) scopeCounts() map[string]int {
	if s.live == nil {
		return nil // 索引を持たない構成（テストの一部）では件数を出さない
	}
	ix := s.live.Current()
	counts := make(map[string]int, len(assistant.Teams)*4)
	origins := []string{"", "wiki", "site", "fee"}
	teams := make([]string, 0, len(assistant.Teams)+1)
	teams = append(teams, "")
	for _, team := range assistant.Teams {
		teams = append(teams, team.Value)
	}
	for i := range ix.Pages {
		page := &ix.Pages[i]
		if len(page.Chunks) == 0 {
			continue
		}
		source := page.Source
		if source == "" {
			source = "wiki" // 旧い index.json には source が無い
		}
		for _, origin := range origins {
			if origin != "" && origin != source {
				continue
			}
			for _, team := range teams {
				if assistant.TeamMatches(team, page.Team) {
					counts[origin+"/"+team]++
				}
			}
		}
	}
	return counts
}

func (s *Server) handleCreateAssistant(w http.ResponseWriter, r *http.Request) {
	var body state.Assistant
	// アイコン画像（data URI・最大96KB）を含むので、他のAPIより大きく取る
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAssistantBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "リクエストが不正です"})
		return
	}
	user, _ := s.currentUser(r)

	// 作成者は本人で固定する。ここを本文から取ると、他人の名前で
	// アシスタントを作れてしまい、名前が出ることによる抑止が消える
	body.Author = user
	now := time.Now().UTC().Format(time.RFC3339)
	body.CreatedAt, body.UpdatedAt = now, now
	if err := assistant.Validate(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// 件数の上限だけは一覧で見る。多少超えても実害が無いので、
	// ここは競合を許す（IDの重複と違い、他人のものを奪う経路にならない）
	list, err := s.state.ListAssistants(r.Context())
	if err != nil {
		log.Printf("アシスタントの読み込みに失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "アシスタントを読み込めませんでした"})
		return
	}
	if len(list) >= maxAssistants {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "アシスタントの数が上限に達しています"})
		return
	}

	// ID重複の判定は書き込み側に委ねる。一覧で調べてから書くと、同時に
	// 作られた2件が両方とも検査を通り、後勝ちで他人のものを上書きできる
	if err := s.state.CreateAssistant(r.Context(), body); errors.Is(err, state.ErrAssistantExists) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": state.ErrAssistantExists.Error()})
		return
	} else if err != nil {
		log.Printf("アシスタントの保存に失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "アシスタントを保存できませんでした"})
		return
	}
	writeJSON(w, http.StatusCreated, s.assistantViews(r.Context(), []state.Assistant{body}, user)[0])
}

// handleUpdateAssistant は作成者本人（と管理者）だけの編集を受け付ける。
//
// IDと作成者と作成日時は引き継ぐ。IDを変えられるようにすると、選択中の
// 利用者の設定が黙って外れる。作成者を変えられるようにすると、
// 「誰が作ったか」を出すことによる抑止が消える。
func (s *Server) handleUpdateAssistant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body state.Assistant
	// アイコン画像（data URI・最大96KB）を含むので、他のAPIより大きく取る
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAssistantBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "リクエストが不正です"})
		return
	}
	user, _ := s.currentUser(r)

	list, err := s.state.ListAssistants(r.Context())
	if err != nil {
		log.Printf("アシスタントの読み込みに失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "アシスタントを読み込めませんでした"})
		return
	}
	for _, current := range list {
		if current.ID != id {
			continue
		}
		if !assistant.CanEdit(current, user, s.isAdmin(r.Context(), user)) {
			writeJSON(w, http.StatusForbidden,
				map[string]string{"error": "作成者本人と管理者だけが編集できます"})
			return
		}
		updated := current // 変えてよい項目だけを上書きする
		updated.Name, updated.Description, updated.Instruction = body.Name, body.Description, body.Instruction
		updated.Team, updated.Origin, updated.Icon = body.Team, body.Origin, body.Icon
		updated.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := assistant.Validate(&updated); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := s.state.UpdateAssistant(r.Context(), updated); err != nil {
			log.Printf("アシスタントの更新に失敗: %v", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "アシスタントを保存できませんでした"})
			return
		}
		writeJSON(w, http.StatusOK, s.assistantViews(r.Context(), []state.Assistant{updated}, user)[0])
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "アシスタントが見つかりません"})
}

func (s *Server) handleDeleteAssistant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user, _ := s.currentUser(r)
	list, err := s.state.ListAssistants(r.Context())
	if err != nil {
		log.Printf("アシスタントの読み込みに失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "アシスタントを読み込めませんでした"})
		return
	}
	for _, item := range list {
		if item.ID != id {
			continue
		}
		if !assistant.CanEdit(item, user, s.isAdmin(r.Context(), user)) {
			writeJSON(w, http.StatusForbidden,
				map[string]string{"error": "作成者本人と管理者だけが削除できます"})
			return
		}
		if err := s.state.DeleteAssistant(r.Context(), id); err != nil {
			log.Printf("アシスタントの削除に失敗: %v", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "アシスタントを削除できませんでした"})
			return
		}
		if user != item.Author && s.isAdmin(r.Context(), user) {
			s.saveAdminAudit(r.Context(), user, "assistant.delete", id)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "アシスタントが見つかりません"})
}

// errAssistantUnavailable は、指定されたアシスタントを解決できなかったことを表す。
var errAssistantUnavailable = errors.New("アシスタントを特定できません")

// lookupAssistant は質問に添えられたIDを実体に解決する。
//
// ⚠️ **解決できないときに「未選択」へ落とさない。** 以前はFirestore障害や
// 削除直後に nil を返しており、参照範囲を絞ったつもりの質問が全資料参照へ
// 黙って広がっていた。範囲を安全性の境界として使う以上、ここは閉じる側に倒す。
func (s *Server) lookupAssistant(ctx context.Context, id string) (*state.Assistant, error) {
	if id == "" {
		return nil, nil // 明示的な「汎用」。絞り込み無しで正しい
	}
	list, err := s.state.ListAssistants(ctx)
	if err != nil {
		log.Printf("アシスタントの読み込みに失敗: %v", err)
		return nil, err
	}
	for _, item := range list {
		if item.ID == id {
			return &item, nil
		}
	}
	return nil, errAssistantUnavailable
}

// ---------------------------------------------------------------- 質問

func validConversationContext(context []pipeline.ConversationTurn) bool {
	// 全履歴を毎回送ると入力費用が会話の長さに比例して増える。指示語の解決には
	// 直近2往復で足りるため、サーバー側でも上限を固定する。
	if len(context) > maxConversationTurns {
		return false
	}
	for _, turn := range context {
		if strings.TrimSpace(turn.Question) == "" || strings.TrimSpace(turn.Answer) == "" ||
			len([]rune(turn.Question)) > maxConversationQuestionRunes || len([]rune(turn.Answer)) > maxConversationAnswerRunes {
			return false
		}
	}
	return true
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Question     string                      `json:"question"`
		AssistantID  string                      `json:"assistantId"`
		ResponseMode string                      `json:"responseMode"`
		Context      []pipeline.ConversationTurn `json:"context"`
		// data URI の配列。画面側が長辺768pxのJPEGへ落としてから送る
		Attachments []string `json:"attachments"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAskBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "リクエストが不正です"})
		return
	}
	// 質問をURLに載せない。非公開Wikiに関する文面がアクセスログへ残るのを避けるため。
	question := strings.TrimSpace(body.Question)
	images, err := parseAttachments(body.Attachments)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": strings.TrimPrefix(err.Error(), "添付が不正です: ")})
		return
	}
	// 画像だけを添えて送れる。「これでツイート作って」のように、
	// 文章より画像のほうが本体である使い方があるため
	if question == "" && len(images) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "質問を入力してください（500文字以内）"})
		return
	}
	if len([]rune(question)) > maxQuestionRunes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "質問を入力してください（500文字以内）"})
		return
	}
	if !validConversationContext(body.Context) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "直近の会話が不正です"})
		return
	}
	responseMode, ok := pipeline.ParseResponseMode(body.ResponseMode)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "回答モードが不正です"})
		return
	}

	// 解決はレート制限の前に済ませる。ここで止める場合に回数を消費させないため。
	selected, err := s.lookupAssistant(r.Context(), body.AssistantID)
	if err != nil {
		message := "選んだアシスタントが見つかりません。一覧から選び直してください"
		if !errors.Is(err, errAssistantUnavailable) {
			message = "アシスタントを読み込めませんでした。時間を置いてお試しください"
		}
		// 汎用へ落として続行しない。参照範囲を絞ったつもりの質問が
		// 全資料参照へ広がるより、答えないほうが安全
		writeJSON(w, http.StatusConflict, map[string]string{"error": message})
		return
	}

	// レート制限。本当の費用リスクはインフラではなく
	// API従量課金である（docs/01-設計方針.md §7）。これが実質的な上限装置になる。
	user, _ := s.currentUser(r)
	userKey := s.userKey(user)
	// 確保した日を1度だけ決めて、返却でも同じ日を使う。呼ぶたびに today() を
	// 評価すると、23:59台に確保して0時以降に失敗したときへ翌日分を返してしまう
	day := today()
	taken, err := s.state.Take(r.Context(), userKey, day, s.cfg.DailyLimit)
	if err != nil {
		log.Printf("利用回数の確保に失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "利用回数を確認できませんでした。時間を置いてお試しください"})
		return
	}
	if !taken {
		now := time.Now().UTC()
		s.saveUsageEvent(state.UsageEvent{
			ID: s.eventID("usage", userKey, now), UserKey: userKey, OccurredAt: now,
			Outcome: "user_daily_limit", ResponseMode: string(responseMode),
			AssistantID: body.AssistantID, HasAttachment: len(images) > 0,
		})
		writeJSON(w, http.StatusTooManyRequests,
			map[string]string{
				"error":    "本日の質問回数の上限に達しました。日本時間の0時以降にもう一度お試しください",
				"code":     "user_daily_limit",
				"retry_at": nextJapanMidnight().Format(time.RFC3339),
			})
		return
	}
	usageStarted := time.Now().UTC()
	usageEvent := state.UsageEvent{
		ID: s.eventID("usage", userKey, usageStarted), UserKey: userKey,
		OccurredAt: usageStarted, Outcome: "success", ResponseMode: string(responseMode),
		AssistantID: body.AssistantID, HasAttachment: len(images) > 0,
	}
	defer func() {
		usageEvent.DurationMS = time.Since(usageStarted).Milliseconds()
		s.saveUsageEvent(usageEvent)
	}()

	flusher, ok := w.(http.Flusher)
	if !ok {
		usageEvent.Outcome = "unavailable"
		if err := s.refund(r.Context(), userKey, day); err != nil {
			log.Printf("利用回数の返却に失敗: %v", err)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "ストリーミング未対応"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	var mu sync.Mutex
	emit := func(e pipeline.Event) {
		if e.Type == "mode" {
			usageEvent.ResolvedMode = e.Mode
		}
		mu.Lock()
		defer mu.Unlock()
		payload, err := json.Marshal(e)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}

	if err := s.pipe.RunWithImages(r.Context(), question, body.Context, selected, responseMode, images, emit); err != nil {
		log.Printf("質問の処理に失敗: %v", err)
		message := "回答の生成に失敗しました"
		code := ""
		retryAt := ""
		if errors.Is(err, llm.ErrDailyQuota) {
			usageEvent.Outcome = "daily_quota"
			if refundErr := s.refund(r.Context(), userKey, day); refundErr != nil {
				log.Printf("利用回数の返却に失敗: %v", refundErr)
			}
			code = "daily_quota"
			message = "Gemini無料枠の本日分を使い切りました。Google側の上限がリセットされてからお試しください"
		} else if errors.Is(err, llm.ErrRateLimited) {
			usageEvent.Outcome = "rate_limit"
			if refundErr := s.refund(r.Context(), userKey, day); refundErr != nil {
				log.Printf("利用回数の返却に失敗: %v", refundErr)
			}
			code = "rate_limit"
			message = "アクセスが集中し、Geminiの短時間の利用上限に達しました。数分後にもう一度お試しください"
		} else if errors.Is(err, llm.ErrImagesUnsupported) {
			usageEvent.Outcome = "images_unsupported"
			// 画像を読めないモデルで受けた場合。利用者の質問の責任ではないので回数を返す
			if refundErr := s.refund(r.Context(), userKey, day); refundErr != nil {
				log.Printf("利用回数の返却に失敗: %v", refundErr)
			}
			message = "いま動いているモデルは画像を読めません。画像を外してもう一度お試しください"
		} else if errors.Is(err, llm.ErrUnavailable) {
			usageEvent.Outcome = "unavailable"
			// 利用者ではなくGemini側の都合で失敗した質問は、個人の利用回数へ含めない。
			if refundErr := s.refund(r.Context(), userKey, day); refundErr != nil {
				log.Printf("利用回数の返却に失敗: %v", refundErr)
			}
			code = "unavailable"
			message = "現在Geminiを利用できません。時間を置いてからもう一度お試しください"
		} else if errors.Is(err, context.Canceled) {
			usageEvent.Outcome = "cancelled"
		} else {
			usageEvent.Outcome = "failed"
		}
		// 従来どおり、Gemini都合以外の失敗は確保した回数へ含める。
		if at, ok := llm.RetryAt(err); ok {
			retryAt = at.Format(time.RFC3339)
		}
		emit(pipeline.Event{Type: "error", Message: message, Code: code, RetryAt: retryAt})
		return
	}
}

// ---------------------------------------------------------------- 補助

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self' data:; img-src 'self' data:; connect-src 'self' https:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		// CORSヘッダーだけでは、レスポンスを読めなくするだけでPOST自体は届く。
		// CookieをSameSite=Noneで使うため、本番ではOriginも照合してCSRFを防ぐ。
		origin := r.Header.Get("Origin")
		if origin != "" && s.cfg.AllowOrigin != "" && !originAllowed(origin, s.cfg.AllowOrigin) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "許可されていない送信元です"})
			return
		}
		if origin != "" && s.cfg.AllowOrigin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func originAllowed(origin, configured string) bool {
	actual, ok := parseOrigin(origin)
	if !ok {
		return false
	}
	for raw := range strings.SplitSeq(configured, ",") {
		allowed, ok := parseOrigin(raw)
		if !ok || actual.Scheme != allowed.Scheme || effectivePort(actual) != effectivePort(allowed) {
			continue
		}
		actualHost, allowedHost := strings.ToLower(actual.Hostname()), strings.ToLower(allowed.Hostname())
		if actualHost == allowedHost || isLoopbackHost(actualHost) && isLoopbackHost(allowedHost) {
			return true
		}
	}
	return false
}

func parseOrigin(raw string) (*url.URL, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, false
	}
	return parsed, true
}

func effectivePort(origin *url.URL) string {
	if port := origin.Port(); port != "" {
		return port
	}
	if origin.Scheme == "https" {
		return "443"
	}
	return "80"
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// 画面の「本日」と一致させるため、Cloud RunのUTCではなく日本時間で日付を切り替える。
var japanTime = time.FixedZone("JST", 9*60*60)

func today() string { return time.Now().In(japanTime).Format("2006-01-02") }

// refund は確保した1回分を戻す。
//
// ⚠️ **リクエストのcontextをそのまま使わない。** 返却が必要になる場面の多くは
// 利用者の切断であり、その時点で r.Context() はキャンセル済みである。
// メモリ実装はcontextを見ないのでテストは通るが、Firestoreはトランザクションを
// 開始できず、返却が黙って失敗していた。値（認証情報など）は引き継ぎ、
// キャンセルだけ切り離した短い期限のcontextで実行する。
func (s *Server) refund(parent context.Context, userKey, day string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
	defer cancel()
	return s.state.Refund(ctx, userKey, day)
}

func nextJapanMidnight() time.Time {
	now := time.Now().In(japanTime)
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, japanTime)
}
