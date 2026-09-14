package state

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Memoryはローカル開発と単体テスト用。Cloud RunではFirestoreを必須にする。
type Memory struct {
	mu          sync.Mutex
	usage       map[string]int
	usageAt     map[string]time.Time
	profiles    map[string]UserProfile
	settings    map[string]UserSettings
	usageEvents map[string]UsageEvent
	adminAudits map[string]AdminAudit
	adminRoles  map[string]AdminRole
	sourceCheck *SourceCheck
	apiUsage    map[string]APIUsage
	chats       map[string]map[string]Chat
	feedback    map[string]Feedback
	assistants  map[string]Assistant
}

func NewMemory() *Memory {
	return &Memory{
		usage:       map[string]int{},
		usageAt:     map[string]time.Time{},
		profiles:    map[string]UserProfile{},
		settings:    map[string]UserSettings{},
		usageEvents: map[string]UsageEvent{},
		adminAudits: map[string]AdminAudit{},
		adminRoles:  map[string]AdminRole{},
		apiUsage:    map[string]APIUsage{},
		chats:       map[string]map[string]Chat{},
		feedback:    map[string]Feedback{},
		assistants:  map[string]Assistant{},
	}
}

func usageKey(user, day string) string { return user + "\x00" + day }

func (m *Memory) Remaining(_ context.Context, user, day string, limit int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return max(limit-m.usage[usageKey(user, day)], 0), nil
}

func (m *Memory) Take(_ context.Context, user, day string, limit int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := usageKey(user, day)
	if m.usage[key] >= limit {
		return false, nil
	}
	m.usage[key]++
	m.usageAt[key] = time.Now().UTC()
	return true, nil
}

func (m *Memory) Refund(_ context.Context, user, day string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := usageKey(user, day)
	if m.usage[key] > 0 {
		m.usage[key]--
		m.usageAt[key] = time.Now().UTC()
	}
	return nil
}

func (m *Memory) SaveUserProfile(_ context.Context, key, username string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	profile, exists := m.profiles[key]
	if !exists {
		profile = UserProfile{Key: key, FirstSeen: at}
	}
	profile.Username = username
	profile.LastSeen = at
	m.profiles[key] = profile
	return nil
}

func (m *Memory) GetUserProfile(_ context.Context, key string) (UserProfile, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	profile, ok := m.profiles[key]
	return profile, ok, nil
}

func (m *Memory) SaveUserIcon(_ context.Context, key, icon string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	profile, ok := m.profiles[key]
	if !ok {
		return fmt.Errorf("利用者プロフィールがありません")
	}
	profile.Icon = icon
	m.profiles[key] = profile
	return nil
}

func (m *Memory) GetUserSettings(_ context.Context, key string) (UserSettings, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	settings, ok := m.settings[key]
	return cloneUserSettings(settings), ok, nil
}

func (m *Memory) SaveUserSettings(_ context.Context, key string, settings UserSettings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	settings.Key = key
	m.settings[key] = cloneUserSettings(settings)
	return nil
}

// cloneUserSettings は取り出した設定を呼び出し側が書き換えても、
// 保存してあるものが変わらないようにする（AdminRole と同じ扱い）。
func cloneUserSettings(settings UserSettings) UserSettings {
	settings.Tools = slices.Clone(settings.Tools)
	settings.DiscordGuilds = slices.Clone(settings.DiscordGuilds)
	return settings
}

func (m *Memory) ListUserProfiles(_ context.Context) ([]UserProfile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]UserProfile, 0, len(m.profiles))
	for _, profile := range m.profiles {
		list = append(list, profile)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Username < list[j].Username })
	return list, nil
}

func (m *Memory) ListDailyUsage(_ context.Context, user, since string) ([]DailyUsage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := user + "\x00"
	var list []DailyUsage
	for key, used := range m.usage {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		day := strings.TrimPrefix(key, prefix)
		if day >= since {
			list = append(list, DailyUsage{Day: day, Used: used, UpdatedAt: m.usageAt[key]})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Day > list[j].Day })
	return list, nil
}

func (m *Memory) PurgeDailyUsage(_ context.Context, user, before string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := user + "\x00"
	removed := 0
	for key := range m.usage {
		if !strings.HasPrefix(key, prefix) || strings.TrimPrefix(key, prefix) >= before {
			continue
		}
		delete(m.usage, key)
		delete(m.usageAt, key)
		removed++
	}
	return removed, nil
}

func (m *Memory) PurgeUserProfiles(_ context.Context, before time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0
	for key, profile := range m.profiles {
		if profile.LastSeen.Before(before) {
			delete(m.profiles, key)
			// 連携設定は本番（Firestore）では同じ文書に入っており、
			// 利用者を消せば一緒に消える。ここでも同じ結果にする
			delete(m.settings, key)
			removed++
		}
	}
	return removed, nil
}

func (m *Memory) SaveUsageEvent(_ context.Context, event UsageEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usageEvents[event.ID] = event
	return nil
}

func (m *Memory) ListUsageEvents(_ context.Context, limit int) ([]UsageEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]UsageEvent, 0, len(m.usageEvents))
	for _, event := range m.usageEvents {
		list = append(list, event)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].OccurredAt.After(list[j].OccurredAt) })
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return list, nil
}

func (m *Memory) PurgeUsageEvents(_ context.Context, before time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0
	for id, event := range m.usageEvents {
		if event.OccurredAt.Before(before) {
			delete(m.usageEvents, id)
			removed++
		}
	}
	return removed, nil
}

func (m *Memory) SaveAdminAudit(_ context.Context, audit AdminAudit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.adminAudits[audit.ID] = audit
	return nil
}

func (m *Memory) ListAdminAudits(_ context.Context, limit int) ([]AdminAudit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]AdminAudit, 0, len(m.adminAudits))
	for _, audit := range m.adminAudits {
		list = append(list, audit)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].OccurredAt.After(list[j].OccurredAt) })
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return list, nil
}

func (m *Memory) PurgeAdminAudits(_ context.Context, before time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0
	for id, audit := range m.adminAudits {
		if audit.OccurredAt.Before(before) {
			delete(m.adminAudits, id)
			removed++
		}
	}
	return removed, nil
}

func (m *Memory) GetAdminRole(_ context.Context, key string) (AdminRole, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	role, ok := m.adminRoles[key]
	return cloneAdminRole(role), ok, nil
}

func (m *Memory) ListAdminRoles(_ context.Context) ([]AdminRole, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]AdminRole, 0, len(m.adminRoles))
	for key, role := range m.adminRoles {
		role.Key = key
		list = append(list, cloneAdminRole(role))
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Username < list[j].Username })
	return list, nil
}

func (m *Memory) SaveAdminRole(_ context.Context, key string, role AdminRole) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	role.Key = key
	m.adminRoles[key] = cloneAdminRole(role)
	return nil
}

func (m *Memory) DeleteAdminRole(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.adminRoles, key)
	return nil
}

func (m *Memory) LatestSourceCheck(_ context.Context) (SourceCheck, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sourceCheck == nil {
		return SourceCheck{}, false, nil
	}
	return cloneSourceCheck(*m.sourceCheck), true, nil
}

func (m *Memory) SaveSourceCheck(_ context.Context, check SourceCheck) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cloned := cloneSourceCheck(check)
	m.sourceCheck = &cloned
	return nil
}

func (m *Memory) ReserveAPIRequest(_ context.Context, day, model string, limit int, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := usageKey(day, model)
	item := m.apiUsage[key]
	if limit > 0 && item.Requests >= limit {
		return false, nil
	}
	item.Day = day
	item.Model = model
	item.Requests++
	item.UpdatedAt = at
	m.apiUsage[key] = item
	return true, nil
}

func (m *Memory) ListAPIUsage(_ context.Context, day string) ([]APIUsage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var list []APIUsage
	for _, item := range m.apiUsage {
		if item.Day == day {
			list = append(list, item)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Model < list[j].Model })
	return list, nil
}

func (m *Memory) ListChats(_ context.Context, user string, limit int) ([]Chat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var chats []Chat
	for _, chat := range m.chats[user] {
		chats = append(chats, cloneChat(chat))
	}
	sort.Slice(chats, func(i, j int) bool { return chats[i].UpdatedAt > chats[j].UpdatedAt })
	if len(chats) > limit {
		chats = chats[:limit]
	}
	return chats, nil
}

func (m *Memory) SaveChat(_ context.Context, user string, chat Chat, limit int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.chats[user] == nil {
		m.chats[user] = map[string]Chat{}
	}
	m.chats[user][chat.ID] = cloneChat(chat)
	if len(m.chats[user]) <= limit {
		return nil
	}
	var chats []Chat
	for _, item := range m.chats[user] {
		chats = append(chats, item)
	}
	sort.Slice(chats, func(i, j int) bool { return chats[i].UpdatedAt > chats[j].UpdatedAt })
	for _, item := range chats[limit:] {
		delete(m.chats[user], item.ID)
	}
	return nil
}

func (m *Memory) DeleteChat(_ context.Context, user, chatID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.chats[user], chatID)
	return nil
}

func (m *Memory) SaveFeedback(_ context.Context, feedback Feedback) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.feedback[feedback.ID] = cloneFeedback(feedback)
	return nil
}

func (m *Memory) PurgeFeedback(_ context.Context, before string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0
	for id, item := range m.feedback {
		// SubmittedAtはRFC3339のUTC固定なので、文字列の辞書順が時刻順と一致する。
		if item.SubmittedAt < before {
			delete(m.feedback, id)
			removed++
		}
	}
	return removed, nil
}

func (m *Memory) ListFeedback(_ context.Context, limit int) ([]Feedback, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]Feedback, 0, len(m.feedback))
	for _, item := range m.feedback {
		list = append(list, cloneFeedback(item))
	}
	sort.Slice(list, func(i, j int) bool { return list[i].SubmittedAt > list[j].SubmittedAt })
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return list, nil
}

func (m *Memory) ListAssistants(_ context.Context) ([]Assistant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]Assistant, 0, len(m.assistants))
	for _, item := range m.assistants {
		list = append(list, cloneAssistant(item))
	}
	// 作成順に並べる。人気順や利用回数での並べ替えは、数が増えて
	// 探しづらくなってから足す（いまは10件程度を想定している）
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt < list[j].CreatedAt })
	return list, nil
}

// SaveAssistant は無条件に書き込む。シード投入と下準備のためだけに使う。
func (m *Memory) SaveAssistant(_ context.Context, assistant Assistant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assistants[assistant.ID] = cloneAssistant(assistant)
	return nil
}

func (m *Memory) CreateAssistant(_ context.Context, assistant Assistant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// 検査と書き込みを同じロックの中で行う。分けると同時作成で両方通る
	if _, exists := m.assistants[assistant.ID]; exists {
		return ErrAssistantExists
	}
	m.assistants[assistant.ID] = cloneAssistant(assistant)
	return nil
}

func (m *Memory) UpdateAssistant(_ context.Context, assistant Assistant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.assistants[assistant.ID]; !exists {
		return ErrAssistantNotFound
	}
	m.assistants[assistant.ID] = cloneAssistant(assistant)
	return nil
}

func (m *Memory) DeleteAssistant(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.assistants, id)
	return nil
}

// Firestoreは直列化を挟むため、保存値と呼び出し側の可変なスライスは共有されない。
// Memoryでも同じ性質を保ち、テストと本番で後から値が変わる条件を揃える。
func cloneAdminRole(role AdminRole) AdminRole {
	role.Tools = slices.Clone(role.Tools)
	return role
}

func cloneSourceCheck(check SourceCheck) SourceCheck {
	check.Deltas = slices.Clone(check.Deltas)
	for i := range check.Deltas {
		check.Deltas[i].Added = slices.Clone(check.Deltas[i].Added)
		check.Deltas[i].Updated = slices.Clone(check.Deltas[i].Updated)
		check.Deltas[i].Removed = slices.Clone(check.Deltas[i].Removed)
	}
	return check
}

func cloneChat(chat Chat) Chat {
	chat.Turns = slices.Clone(chat.Turns)
	for i := range chat.Turns {
		chat.Turns[i].Sources = slices.Clone(chat.Turns[i].Sources)
		chat.Turns[i].FeedbackReasons = slices.Clone(chat.Turns[i].FeedbackReasons)
		if chat.Turns[i].Timings != nil {
			timings := *chat.Turns[i].Timings
			chat.Turns[i].Timings = &timings
		}
	}
	return chat
}

func cloneFeedback(feedback Feedback) Feedback {
	feedback.Reasons = slices.Clone(feedback.Reasons)
	feedback.Sources = slices.Clone(feedback.Sources)
	if feedback.Timings != nil {
		timings := *feedback.Timings
		feedback.Timings = &timings
	}
	return feedback
}

func cloneAssistant(assistant Assistant) Assistant {
	assistant.Glossary = slices.Clone(assistant.Glossary)
	return assistant
}
