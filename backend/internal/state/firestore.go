package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Firestore struct {
	client *firestore.Client
}

func NewFirestore(ctx context.Context, projectID string) (*Firestore, error) {
	client, err := firestore.NewClient(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return &Firestore{client: client}, nil
}

func (f *Firestore) Close() error { return f.client.Close() }

func (f *Firestore) usageDoc(user, day string) *firestore.DocumentRef {
	return f.client.Collection("users").Doc(user).Collection("usage").Doc(day)
}

func usedFromSnapshot(snapshot *firestore.DocumentSnapshot) int {
	value, err := snapshot.DataAt("used")
	if err != nil {
		return 0
	}
	switch used := value.(type) {
	case int64:
		return int(used)
	case int:
		return used
	default:
		return 0
	}
}

func (f *Firestore) Remaining(ctx context.Context, user, day string, limit int) (int, error) {
	snapshot, err := f.usageDoc(user, day).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return limit, nil
	}
	if err != nil {
		return 0, err
	}
	return max(limit-usedFromSnapshot(snapshot), 0), nil
}

func (f *Firestore) Take(ctx context.Context, user, day string, limit int) (bool, error) {
	ref := f.usageDoc(user, day)
	taken := false
	err := f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		taken = false
		used := 0
		snapshot, err := tx.Get(ref)
		if err == nil {
			used = usedFromSnapshot(snapshot)
		} else if status.Code(err) != codes.NotFound {
			return err
		}
		if used >= limit {
			return nil
		}
		taken = true
		return tx.Set(ref, map[string]any{
			"used":       used + 1,
			"updated_at": time.Now().UTC(),
		})
	})
	return taken, err
}

func (f *Firestore) Refund(ctx context.Context, user, day string) error {
	ref := f.usageDoc(user, day)
	return f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		used := usedFromSnapshot(snapshot)
		if used == 0 {
			return nil
		}
		return tx.Set(ref, map[string]any{
			"used":       used - 1,
			"updated_at": time.Now().UTC(),
		})
	})
}

func (f *Firestore) SaveUserProfile(ctx context.Context, key, username string, at time.Time) error {
	ref := f.client.Collection("users").Doc(key)
	return f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		firstSeen := at
		snapshot, err := tx.Get(ref)
		if err == nil {
			if value, err := snapshot.DataAt("first_seen"); err == nil {
				if saved, ok := value.(time.Time); ok {
					firstSeen = saved
				}
			}
		} else if status.Code(err) != codes.NotFound {
			return err
		}
		// 利用者画像は別の操作で更新するため、ログインやセッション確認のたびに
		// プロフィール全体を上書きして画像を消さない。
		return tx.Set(ref, map[string]any{
			"username": username, "first_seen": firstSeen, "last_seen": at,
		}, firestore.MergeAll)
	})
}

func (f *Firestore) GetUserProfile(ctx context.Context, key string) (UserProfile, bool, error) {
	snapshot, err := f.client.Collection("users").Doc(key).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return UserProfile{}, false, nil
	}
	if err != nil {
		return UserProfile{}, false, err
	}
	var profile UserProfile
	if err := snapshot.DataTo(&profile); err != nil {
		return UserProfile{}, false, err
	}
	profile.Key = snapshot.Ref.ID
	return profile, true, nil
}

func (f *Firestore) SaveUserIcon(ctx context.Context, key, icon string) error {
	ref := f.client.Collection("users").Doc(key)
	if icon == "" {
		_, err := ref.Update(ctx, []firestore.Update{{Path: "icon", Value: firestore.Delete}})
		return err
	}
	_, err := ref.Update(ctx, []firestore.Update{{Path: "icon", Value: icon}})
	return err
}

func (f *Firestore) ListUserProfiles(ctx context.Context) ([]UserProfile, error) {
	snapshots, err := f.client.Collection("users").Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	list := make([]UserProfile, 0, len(snapshots))
	for _, snapshot := range snapshots {
		var profile UserProfile
		if err := snapshot.DataTo(&profile); err != nil {
			return nil, err
		}
		// 旧来はusers直下にドキュメントを置かなかったため、名前のあるものだけを返す。
		if profile.Username == "" {
			continue
		}
		profile.Key = snapshot.Ref.ID
		list = append(list, profile)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Username < list[j].Username })
	return list, nil
}

func (f *Firestore) ListDailyUsage(ctx context.Context, user, since string) ([]DailyUsage, error) {
	snapshots, err := f.client.Collection("users").Doc(user).Collection("usage").Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	var list []DailyUsage
	for _, snapshot := range snapshots {
		if snapshot.Ref.ID < since {
			continue
		}
		var usage DailyUsage
		if err := snapshot.DataTo(&usage); err != nil {
			return nil, err
		}
		usage.Day = snapshot.Ref.ID
		list = append(list, usage)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Day > list[j].Day })
	return list, nil
}

func (f *Firestore) PurgeDailyUsage(ctx context.Context, user, before string) (int, error) {
	snapshots, err := f.client.Collection("users").Doc(user).Collection("usage").Documents(ctx).GetAll()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, snapshot := range snapshots {
		if snapshot.Ref.ID >= before || removed >= maxAdminPurge {
			continue
		}
		if _, err := snapshot.Ref.Delete(ctx); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

const maxAdminPurge = 100

func (f *Firestore) PurgeUserProfiles(ctx context.Context, before time.Time) (int, error) {
	snapshots, err := f.client.Collection("users").Where("last_seen", "<", before).
		Limit(maxAdminPurge).Documents(ctx).GetAll()
	if err != nil {
		return 0, err
	}
	for _, snapshot := range snapshots {
		// 子のusageはHMACだけで実名を持たない。親だけ消せば実名との対応は失われる。
		if _, err := snapshot.Ref.Delete(ctx); err != nil {
			return 0, err
		}
	}
	return len(snapshots), nil
}

func (f *Firestore) SaveUsageEvent(ctx context.Context, event UsageEvent) error {
	_, err := f.client.Collection("usage_events").Doc(event.ID).Set(ctx, event)
	return err
}

func (f *Firestore) ListUsageEvents(ctx context.Context, limit int) ([]UsageEvent, error) {
	query := f.client.Collection("usage_events").OrderBy("occurred_at", firestore.Desc)
	if limit > 0 {
		query = query.Limit(limit)
	}
	snapshots, err := query.Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	list := make([]UsageEvent, 0, len(snapshots))
	for _, snapshot := range snapshots {
		var event UsageEvent
		if err := snapshot.DataTo(&event); err != nil {
			return nil, err
		}
		list = append(list, event)
	}
	return list, nil
}

func (f *Firestore) PurgeUsageEvents(ctx context.Context, before time.Time) (int, error) {
	return f.purgeTimedCollection(ctx, "usage_events", "occurred_at", before)
}

func (f *Firestore) SaveAdminAudit(ctx context.Context, audit AdminAudit) error {
	_, err := f.client.Collection("admin_audits").Doc(audit.ID).Set(ctx, audit)
	return err
}

func (f *Firestore) ListAdminAudits(ctx context.Context, limit int) ([]AdminAudit, error) {
	query := f.client.Collection("admin_audits").OrderBy("occurred_at", firestore.Desc)
	if limit > 0 {
		query = query.Limit(limit)
	}
	snapshots, err := query.Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	list := make([]AdminAudit, 0, len(snapshots))
	for _, snapshot := range snapshots {
		var audit AdminAudit
		if err := snapshot.DataTo(&audit); err != nil {
			return nil, err
		}
		list = append(list, audit)
	}
	return list, nil
}

func (f *Firestore) PurgeAdminAudits(ctx context.Context, before time.Time) (int, error) {
	return f.purgeTimedCollection(ctx, "admin_audits", "occurred_at", before)
}

func (f *Firestore) GetAdminRole(ctx context.Context, key string) (AdminRole, bool, error) {
	snapshot, err := f.client.Collection("admin_roles").Doc(key).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return AdminRole{}, false, nil
	}
	if err != nil {
		return AdminRole{}, false, err
	}
	var role AdminRole
	if err := snapshot.DataTo(&role); err != nil {
		return AdminRole{}, false, err
	}
	role.Key = snapshot.Ref.ID
	return role, true, nil
}

func (f *Firestore) ListAdminRoles(ctx context.Context) ([]AdminRole, error) {
	snapshots, err := f.client.Collection("admin_roles").Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	list := make([]AdminRole, 0, len(snapshots))
	for _, snapshot := range snapshots {
		var role AdminRole
		if err := snapshot.DataTo(&role); err != nil {
			return nil, err
		}
		role.Key = snapshot.Ref.ID
		list = append(list, role)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Username < list[j].Username })
	return list, nil
}

func (f *Firestore) SaveAdminRole(ctx context.Context, key string, role AdminRole) error {
	_, err := f.client.Collection("admin_roles").Doc(key).Set(ctx, role)
	return err
}

func (f *Firestore) DeleteAdminRole(ctx context.Context, key string) error {
	_, err := f.client.Collection("admin_roles").Doc(key).Delete(ctx)
	return err
}

func (f *Firestore) LatestSourceCheck(ctx context.Context) (SourceCheck, bool, error) {
	snapshot, err := f.client.Collection("system_state").Doc("source_check").Get(ctx)
	if status.Code(err) == codes.NotFound {
		return SourceCheck{}, false, nil
	}
	if err != nil {
		return SourceCheck{}, false, err
	}
	var check SourceCheck
	if err := snapshot.DataTo(&check); err != nil {
		return SourceCheck{}, false, err
	}
	return check, true, nil
}

func (f *Firestore) SaveSourceCheck(ctx context.Context, check SourceCheck) error {
	_, err := f.client.Collection("system_state").Doc("source_check").Set(ctx, check)
	return err
}

func (f *Firestore) purgeTimedCollection(ctx context.Context, collection, field string, before time.Time) (int, error) {
	snapshots, err := f.client.Collection(collection).Where(field, "<", before).
		Limit(maxAdminPurge).Documents(ctx).GetAll()
	if err != nil {
		return 0, err
	}
	for _, snapshot := range snapshots {
		if _, err := snapshot.Ref.Delete(ctx); err != nil {
			return 0, err
		}
	}
	return len(snapshots), nil
}

func apiUsageID(model string) string {
	sum := sha256.Sum256([]byte(model))
	return hex.EncodeToString(sum[:12])
}

func (f *Firestore) RecordAPIRequest(ctx context.Context, day, model string, at time.Time) error {
	ref := f.client.Collection("api_usage").Doc(day).Collection("models").Doc(apiUsageID(model))
	_, err := ref.Set(ctx, map[string]any{
		"day": day, "model": model, "requests": firestore.Increment(1), "updated_at": at,
	}, firestore.MergeAll)
	return err
}

func (f *Firestore) ListAPIUsage(ctx context.Context, day string) ([]APIUsage, error) {
	snapshots, err := f.client.Collection("api_usage").Doc(day).Collection("models").Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	list := make([]APIUsage, 0, len(snapshots))
	for _, snapshot := range snapshots {
		var usage APIUsage
		if err := snapshot.DataTo(&usage); err != nil {
			return nil, err
		}
		list = append(list, usage)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Model < list[j].Model })
	return list, nil
}

func (f *Firestore) chatCollection(user string) *firestore.CollectionRef {
	return f.client.Collection("users").Doc(user).Collection("chats")
}

func (f *Firestore) ListChats(ctx context.Context, user string, limit int) ([]Chat, error) {
	snapshots, err := f.chatCollection(user).
		OrderBy("updated_at", firestore.Desc).
		Limit(limit).
		Documents(ctx).
		GetAll()
	if err != nil {
		return nil, err
	}
	chats := make([]Chat, 0, len(snapshots))
	for _, snapshot := range snapshots {
		var chat Chat
		if err := snapshot.DataTo(&chat); err != nil {
			return nil, err
		}
		chats = append(chats, chat)
	}
	return chats, nil
}

func (f *Firestore) SaveChat(ctx context.Context, user string, chat Chat, limit int) error {
	collection := f.chatCollection(user)
	if _, err := collection.Doc(chat.ID).Set(ctx, chat); err != nil {
		return err
	}
	snapshots, err := collection.
		OrderBy("updated_at", firestore.Desc).
		Documents(ctx).
		GetAll()
	if err != nil {
		return err
	}
	if len(snapshots) <= limit {
		return nil
	}
	batch := f.client.Batch()
	for _, snapshot := range snapshots[limit:] {
		batch.Delete(snapshot.Ref)
	}
	_, err = batch.Commit(ctx)
	return err
}

func (f *Firestore) DeleteChat(ctx context.Context, user, chatID string) error {
	_, err := f.chatCollection(user).Doc(chatID).Delete(ctx)
	return err
}

func (f *Firestore) SaveFeedback(ctx context.Context, feedback Feedback) error {
	_, err := f.client.Collection("feedback").Doc(feedback.ID).Set(ctx, feedback)
	return err
}

// 保存期間を過ぎた報告を消す。1回で消す件数に上限を置き、溜まっていた場合でも
// 利用者のリクエストを長く待たせない。残りは次回の送信時に消える。
const maxFeedbackPurge = 100

func (f *Firestore) PurgeFeedback(ctx context.Context, before string) (int, error) {
	snapshots, err := f.client.Collection("feedback").
		Where("submitted_at", "<", before).
		Limit(maxFeedbackPurge).
		Documents(ctx).
		GetAll()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, snapshot := range snapshots {
		if _, err := snapshot.Ref.Delete(ctx); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func (f *Firestore) ListFeedback(ctx context.Context, limit int) ([]Feedback, error) {
	query := f.client.Collection("feedback").OrderBy("submitted_at", firestore.Desc)
	if limit > 0 {
		query = query.Limit(limit)
	}
	snapshots, err := query.Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	list := make([]Feedback, 0, len(snapshots))
	for _, snapshot := range snapshots {
		var item Feedback
		if err := snapshot.DataTo(&item); err != nil {
			return nil, err
		}
		list = append(list, item)
	}
	return list, nil
}

// アシスタントは全員で共有するため users/ の下ではなくトップレベルに置く。
func (f *Firestore) assistantCollection() *firestore.CollectionRef {
	return f.client.Collection("assistants")
}

func (f *Firestore) ListAssistants(ctx context.Context) ([]Assistant, error) {
	snapshots, err := f.assistantCollection().
		OrderBy("created_at", firestore.Asc).
		Documents(ctx).
		GetAll()
	if err != nil {
		return nil, err
	}
	list := make([]Assistant, 0, len(snapshots))
	for _, snapshot := range snapshots {
		var assistant Assistant
		if err := snapshot.DataTo(&assistant); err != nil {
			return nil, err
		}
		list = append(list, assistant)
	}
	return list, nil
}

// SaveAssistant は無条件に書き込む。初期アシスタントの投入にだけ使う
// （Apply が事前に一覧で存在を確認しており、競合する呼び出し元がない）。
func (f *Firestore) SaveAssistant(ctx context.Context, assistant Assistant) error {
	_, err := f.assistantCollection().Doc(assistant.ID).Set(ctx, assistant)
	return err
}

// CreateAssistant は Create を使う。存在すればFirestoreがAlreadyExistsで弾くため、
// 同時作成でも後勝ちの上書きが起きない（Setだと両方通ってしまう）。
func (f *Firestore) CreateAssistant(ctx context.Context, assistant Assistant) error {
	_, err := f.assistantCollection().Doc(assistant.ID).Create(ctx, assistant)
	if status.Code(err) == codes.AlreadyExists {
		return ErrAssistantExists
	}
	return err
}

func (f *Firestore) UpdateAssistant(ctx context.Context, assistant Assistant) error {
	_, err := f.assistantCollection().Doc(assistant.ID).Set(ctx, assistant)
	return err
}

func (f *Firestore) DeleteAssistant(ctx context.Context, id string) error {
	_, err := f.assistantCollection().Doc(id).Delete(ctx)
	return err
}

var _ Store = (*Firestore)(nil)
var _ Store = (*Memory)(nil)
