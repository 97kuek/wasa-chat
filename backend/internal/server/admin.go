package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/97kuek/wasa-chat/backend/internal/llm"
	"github.com/97kuek/wasa-chat/backend/internal/state"
)

const (
	adminListLimit = 100
	usageRetention = 90 * 24 * time.Hour
)

type adminUserView struct {
	Username     string     `json:"username"`
	Today        int        `json:"today"`
	SevenDays    int        `json:"sevenDays"`
	ThirtyDays   int        `json:"thirtyDays"`
	LastUsed     *time.Time `json:"lastUsed,omitempty"`
	LimitReached bool       `json:"limitReached"`
	Role         string     `json:"role,omitempty"`
}

type adminRoleView struct {
	Username  string     `json:"username"`
	Role      string     `json:"role"`
	GrantedBy string     `json:"grantedBy,omitempty"`
	GrantedAt *time.Time `json:"grantedAt,omitempty"`
}

type usageEventView struct {
	ID            string    `json:"id"`
	Username      string    `json:"username"`
	OccurredAt    time.Time `json:"occurredAt"`
	Outcome       string    `json:"outcome"`
	ResponseMode  string    `json:"responseMode,omitempty"`
	ResolvedMode  string    `json:"resolvedMode,omitempty"`
	AssistantID   string    `json:"assistantId,omitempty"`
	HasAttachment bool      `json:"hasAttachment,omitempty"`
	DurationMS    int64     `json:"durationMs"`
}

type quotaModelView struct {
	Model     string `json:"model"`
	Requests  int    `json:"requests"`
	Limit     int    `json:"limit"`
	Remaining int    `json:"remaining"`
}

type updateProgressView struct {
	Stage       string     `json:"stage"`
	CheckedAt   *time.Time `json:"checkedAt,omitempty"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	Changes     int        `json:"changes"`
}

// eventID は連番用DBを増やさず、秘密の利用者キーも外へ出さないIDを作る。
// UnixNanoだけでは同時処理で衝突しうるため、用途・対象・時刻を既存のHMACへ通す。
func (s *Server) eventID(kind, target string, at time.Time) string {
	return s.feedbackID(kind, fmt.Sprintf("%s|%d", target, at.UnixNano()))
}

func (s *Server) saveUsageEvent(event state.UsageEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.state.SaveUsageEvent(ctx, event); err != nil {
		log.Printf("利用監査ログの保存に失敗: %v", err)
	}
}

func (s *Server) saveAdminAudit(ctx context.Context, actor, action, target string) {
	now := time.Now().UTC()
	audit := state.AdminAudit{
		ID:    s.eventID("admin", actor+"|"+action+"|"+target, now),
		Actor: actor, Action: action, Target: target, OccurredAt: now,
	}
	if err := s.state.SaveAdminAudit(ctx, audit); err != nil {
		log.Printf("管理者監査ログの保存に失敗: %v", err)
	}
}

func pacificDayAndReset(now time.Time) (string, time.Time) {
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		// time/tzdataを組み込んでいるため通常は通らない。壊れた環境でも
		// 日付が空になって残量全体が表示不能になるより、標準時へ倒す。
		location = time.FixedZone("PST", -8*60*60)
	}
	local := now.In(location)
	reset := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, location)
	return local.Format("2006-01-02"), reset
}

func (s *Server) purgeAdminData(ctx context.Context, now time.Time) {
	checks := []struct {
		name string
		fn   func() (int, error)
	}{
		{"利用監査ログ", func() (int, error) { return s.state.PurgeUsageEvents(ctx, now.Add(-usageRetention)) }},
		{"管理者監査ログ", func() (int, error) { return s.state.PurgeAdminAudits(ctx, now.AddDate(-1, 0, 0)) }},
	}
	for _, check := range checks {
		if removed, err := check.fn(); err != nil {
			log.Printf("期限切れ%sの削除に失敗: %v", check.name, err)
		} else if removed > 0 {
			log.Printf("期限切れ%sを%d件削除", check.name, removed)
		}
	}
}

func (s *Server) handleAdminOverview(w http.ResponseWriter, r *http.Request) {
	actor, _ := s.currentUser(r)
	s.saveAdminAudit(r.Context(), actor, "admin.overview.view", "")
	now := time.Now().UTC()
	s.purgeAdminData(r.Context(), now)

	profiles, err := s.state.ListUserProfiles(r.Context())
	if err != nil {
		log.Printf("管理用利用者一覧の読み込みに失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "利用者一覧を読み込めませんでした"})
		return
	}
	jstNow := now.In(japanTime)
	today := jstNow.Format("2006-01-02")
	sevenStart := jstNow.AddDate(0, 0, -6).Format("2006-01-02")
	thirtyStart := jstNow.AddDate(0, 0, -29).Format("2006-01-02")
	usageCutoff := jstNow.AddDate(0, 0, -89).Format("2006-01-02")
	profileCutoff := now.AddDate(-1, 0, 0)
	dynamicRoles, err := s.state.ListAdminRoles(r.Context())
	if err != nil {
		log.Printf("共同管理者一覧の読み込みに失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "管理者一覧を読み込めませんでした"})
		return
	}
	rolesByName := make(map[string]string, len(s.cfg.AdminUsers)+len(dynamicRoles))
	adminRoles := make([]adminRoleView, 0, len(s.cfg.AdminUsers)+len(dynamicRoles))
	for _, username := range s.cfg.AdminUsers {
		rolesByName[username] = "owner"
		adminRoles = append(adminRoles, adminRoleView{Username: username, Role: "owner"})
	}
	for _, role := range dynamicRoles {
		if _, owner := rolesByName[role.Username]; owner || role.Role != "co_admin" {
			continue
		}
		rolesByName[role.Username] = role.Role
		grantedAt := role.GrantedAt
		adminRoles = append(adminRoles, adminRoleView{
			Username: role.Username, Role: role.Role, GrantedBy: role.GrantedBy, GrantedAt: &grantedAt,
		})
	}
	sort.Slice(adminRoles, func(i, j int) bool {
		if adminRoles[i].Role != adminRoles[j].Role {
			return adminRoles[i].Role == "owner"
		}
		return adminRoles[i].Username < adminRoles[j].Username
	})
	users := make([]adminUserView, 0, len(profiles))
	usernames := make(map[string]string, len(profiles))
	todayQuestions := 0
	activeToday := 0
	for _, profile := range profiles {
		if _, err := s.state.PurgeDailyUsage(r.Context(), profile.Key, usageCutoff); err != nil {
			log.Printf("%sの期限切れ利用回数の削除に失敗: %v", profile.Username, err)
		}
		if profile.LastSeen.Before(profileCutoff) {
			continue
		}
		usernames[profile.Key] = profile.Username
		days, err := s.state.ListDailyUsage(r.Context(), profile.Key, thirtyStart)
		if err != nil {
			log.Printf("%sの利用回数の読み込みに失敗: %v", profile.Username, err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "利用回数を読み込めませんでした"})
			return
		}
		view := adminUserView{Username: profile.Username, Role: rolesByName[profile.Username]}
		for _, day := range days {
			view.ThirtyDays += day.Used
			if day.Day >= sevenStart {
				view.SevenDays += day.Used
			}
			if day.Day == today {
				view.Today = day.Used
			}
			if view.LastUsed == nil || day.UpdatedAt.After(*view.LastUsed) {
				lastUsed := day.UpdatedAt
				view.LastUsed = &lastUsed
			}
		}
		view.LimitReached = s.cfg.DailyLimit > 0 && view.Today >= s.cfg.DailyLimit
		if view.Today > 0 {
			activeToday++
			todayQuestions += view.Today
		}
		users = append(users, view)
	}
	if removed, err := s.state.PurgeUserProfiles(r.Context(), profileCutoff); err != nil {
		log.Printf("期限切れ利用者プロフィールの削除に失敗: %v", err)
	} else if removed > 0 {
		log.Printf("期限切れ利用者プロフィールを%d件削除", removed)
	}
	sort.Slice(users, func(i, j int) bool {
		if users[i].Today != users[j].Today {
			return users[i].Today > users[j].Today
		}
		return users[i].Username < users[j].Username
	})

	events, err := s.state.ListUsageEvents(r.Context(), adminListLimit)
	if err != nil {
		log.Printf("利用監査ログの読み込みに失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "利用監査ログを読み込めませんでした"})
		return
	}
	eventViews := make([]usageEventView, 0, len(events))
	for _, event := range events {
		username := usernames[event.UserKey]
		if username == "" {
			username = "（実名の保存期限切れ）"
		}
		eventViews = append(eventViews, usageEventView{
			ID: event.ID, Username: username, OccurredAt: event.OccurredAt,
			Outcome: event.Outcome, ResponseMode: event.ResponseMode,
			ResolvedMode: event.ResolvedMode, AssistantID: event.AssistantID,
			HasAttachment: event.HasAttachment, DurationMS: event.DurationMS,
		})
	}

	audits, err := s.state.ListAdminAudits(r.Context(), adminListLimit)
	if err != nil {
		log.Printf("管理者監査ログの読み込みに失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "管理者監査ログを読み込めませんでした"})
		return
	}

	pacificDay, resetAt := pacificDayAndReset(now)
	apiUsage, err := s.state.ListAPIUsage(r.Context(), pacificDay)
	if err != nil {
		log.Printf("Gemini利用回数の読み込みに失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "API利用回数を読み込めませんでした"})
		return
	}
	quotaModels := make([]quotaModelView, 0, len(apiUsage))
	totalAPIRequests := 0
	for _, usage := range apiUsage {
		remaining := max(s.cfg.APIDailyLimit-usage.Requests, 0)
		quotaModels = append(quotaModels, quotaModelView{
			Model: usage.Model, Requests: usage.Requests,
			Limit: s.cfg.APIDailyLimit, Remaining: remaining,
		})
		totalAPIRequests += usage.Requests
	}
	// まだ1回も呼んでいない日も、現在モデルの残量を0件の一覧として表示する。
	if len(quotaModels) == 0 && strings.HasPrefix(s.cfg.LLMName, "gemini/") {
		model := strings.TrimPrefix(s.cfg.LLMName, "gemini/")
		quotaModels = append(quotaModels, quotaModelView{
			Model: model, Limit: s.cfg.APIDailyLimit, Remaining: s.cfg.APIDailyLimit,
		})
	}
	runtimeStatus := llm.RuntimeStatus{State: "available"}
	if s.cfg.LLMStatus != nil {
		runtimeStatus = s.cfg.LLMStatus()
	}
	lastSourceCheck, hasSourceCheck, err := s.state.LatestSourceCheck(r.Context())
	if err != nil {
		log.Printf("最終更新確認の読み込みに失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "更新確認の状態を読み込めませんでした"})
		return
	}
	sourceCheckView := map[string]any{
		"available": s.cfg.SourceCheckAvailable, "hasResult": hasSourceCheck,
	}
	if hasSourceCheck {
		sourceCheckView["last"] = lastSourceCheck
	}

	var publishedAt *time.Time
	if parsed, parseErr := time.Parse(time.RFC3339, s.cfg.IndexPublishedAt); parseErr == nil {
		publishedAt = &parsed
	}
	progress := updateProgressView{Stage: "not_checked", PublishedAt: publishedAt}
	if !s.cfg.SourceCheckAvailable {
		progress.Stage = "unavailable"
	} else if hasSourceCheck {
		checkedAt := lastSourceCheck.CheckedAt
		progress.CheckedAt = &checkedAt
		for _, delta := range lastSourceCheck.Deltas {
			progress.Changes += len(delta.Added) + len(delta.Updated) + len(delta.Removed)
		}
		switch {
		case !lastSourceCheck.Changed:
			progress.Stage = "current"
		case publishedAt != nil && publishedAt.After(lastSourceCheck.CheckedAt):
			progress.Stage = "verify_needed"
		default:
			progress.Stage = "changes_detected"
		}
	}

	indexVersion := ""
	if s.live != nil {
		indexVersion = s.live.Current().Version
	}

	quota := map[string]any{
		"day": pacificDay, "resetAt": resetAt, "state": runtimeStatus.State,
		"totalRequests": totalAPIRequests, "estimated": true, "models": quotaModels,
	}
	if !runtimeStatus.RetryAt.IsZero() {
		quota["retryAt"] = runtimeStatus.RetryAt
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"generatedAt": now,
		"system": map[string]any{
			"ok": true, "llm": s.cfg.LLMName,
			"store": s.cfg.StoreName, "indexSource": s.cfg.IndexSource,
			"revision": s.cfg.Revision, "codeVersion": s.cfg.CodeVersion,
			"indexVersion": indexVersion, "indexPublishedAt": s.cfg.IndexPublishedAt,
			"startedAt": s.startedAt,
		},
		"currentAdmin":   map[string]any{"username": actor, "role": rolesByName[actor]},
		"admins":         adminRoles,
		"sourceCheck":    sourceCheckView,
		"updateProgress": progress,
		"quota":          quota,
		"summary": map[string]any{
			"todayQuestions": todayQuestions, "activeUsersToday": activeToday,
			"knownUsers": len(users), "dailyLimit": s.cfg.DailyLimit,
		},
		"users": users, "usageEvents": eventViews, "adminAudits": audits,
	})
}

func (s *Server) handleAdminRole(w http.ResponseWriter, r *http.Request) {
	actor, _ := s.currentUser(r)
	var body struct {
		Username string `json:"username"`
		Enabled  bool   `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSmallRequestBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "リクエストが不正です"})
		return
	}
	username := strings.TrimSpace(body.Username)
	if username == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "利用者を選んでください"})
		return
	}
	if s.isOwner(username) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "主管理者は管理画面から変更できません"})
		return
	}
	key := s.userKey(username)
	if body.Enabled {
		profiles, err := s.state.ListUserProfiles(r.Context())
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "利用者一覧を読み込めませんでした"})
			return
		}
		known := false
		for _, profile := range profiles {
			if profile.Username == username {
				known = true
				break
			}
		}
		if !known {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "WASA Chatへログインしたことのある利用者だけを指定できます"})
			return
		}
		now := time.Now().UTC()
		if err := s.state.SaveAdminRole(r.Context(), key, state.AdminRole{
			Username: username, Role: "co_admin", GrantedBy: actor, GrantedAt: now,
		}); err != nil {
			log.Printf("共同管理者の保存に失敗: %v", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "共同管理者を追加できませんでした"})
			return
		}
		s.saveAdminAudit(r.Context(), actor, "admin.role.grant", username)
	} else {
		if _, exists, err := s.state.GetAdminRole(r.Context(), key); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "管理者ロールを読み込めませんでした"})
			return
		} else if !exists {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "共同管理者ではありません"})
			return
		}
		if err := s.state.DeleteAdminRole(r.Context(), key); err != nil {
			log.Printf("共同管理者の解除に失敗: %v", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "共同管理者を解除できませんでした"})
			return
		}
		s.saveAdminAudit(r.Context(), actor, "admin.role.revoke", username)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSourceCheck(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.SourceCheckAvailable || s.cfg.SourceCheck == nil {
		writeJSON(w, http.StatusServiceUnavailable,
			map[string]string{"error": "更新確認用のWikiアカウントが設定されていません"})
		return
	}
	if !s.sourceMu.TryLock() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "別の更新確認を実行中です"})
		return
	}
	defer s.sourceMu.Unlock()

	actor, _ := s.currentUser(r)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	deltas, err := s.cfg.SourceCheck(ctx)
	if err != nil {
		log.Printf("管理画面からの更新確認に失敗: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	changed := false
	for _, delta := range deltas {
		if len(delta.Added)+len(delta.Updated)+len(delta.Removed) > 0 {
			changed = true
			break
		}
	}
	check := state.SourceCheck{
		CheckedAt: time.Now().UTC(), CheckedBy: actor, Changed: changed, Deltas: deltas,
	}
	if err := s.state.SaveSourceCheck(r.Context(), check); err != nil {
		log.Printf("更新確認結果の保存に失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "更新確認結果を保存できませんでした"})
		return
	}
	s.saveAdminAudit(r.Context(), actor, "source.check", "")
	writeJSON(w, http.StatusOK, check)
}
