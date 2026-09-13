package state

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMemoryUsageIsSharedByUserAndDay(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	for range 3 {
		taken, err := store.Take(ctx, "利用者", "2026-08-08", 3)
		if err != nil || !taken {
			t.Fatalf("質問回数を確保できない: taken=%v err=%v", taken, err)
		}
	}
	if taken, err := store.Take(ctx, "利用者", "2026-08-08", 3); err != nil || taken {
		t.Fatalf("上限を超えて確保した: taken=%v err=%v", taken, err)
	}
	if err := store.Refund(ctx, "利用者", "2026-08-08"); err != nil {
		t.Fatal(err)
	}
	if remaining, _ := store.Remaining(ctx, "利用者", "2026-08-08", 3); remaining != 1 {
		t.Fatalf("返却が反映されていない: remaining=%d", remaining)
	}
	if remaining, _ := store.Remaining(ctx, "利用者", "2026-08-09", 3); remaining != 3 {
		t.Fatalf("日付をまたいで回数が混ざった: remaining=%d", remaining)
	}
	if removed, err := store.PurgeDailyUsage(ctx, "利用者", "2026-08-09"); err != nil || removed != 1 {
		t.Fatalf("期限切れの日次回数だけを消していない: removed=%d err=%v", removed, err)
	}
	if remaining, _ := store.Remaining(ctx, "利用者", "2026-08-08", 3); remaining != 3 {
		t.Fatalf("削除した日の回数が残っている: remaining=%d", remaining)
	}
}

func TestMemoryAdminDataKeepsNamesSeparateFromUsageAndExpires(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	last := first.Add(24 * time.Hour)
	if err := store.SaveUserProfile(ctx, "hmac-key", "42 Wasa Taro", first); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUserProfile(ctx, "hmac-key", "42 Wasa Taro", last); err != nil {
		t.Fatal(err)
	}
	profiles, _ := store.ListUserProfiles(ctx)
	if len(profiles) != 1 || !profiles[0].FirstSeen.Equal(first) || !profiles[0].LastSeen.Equal(last) {
		t.Fatalf("初回・最終利用を保てていない: %+v", profiles)
	}
	if removed, _ := store.PurgeUserProfiles(ctx, last); removed != 0 {
		t.Fatal("保存期限の境界にあるプロフィールを消した")
	}
	if removed, _ := store.PurgeUserProfiles(ctx, last.Add(time.Second)); removed != 1 {
		t.Fatalf("期限切れプロフィールを消していない: %d", removed)
	}
}

func TestMemoryUserProfileKeepsAndRemovesOwnIcon(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	if err := store.SaveUserProfile(ctx, "hmac-key", "42 Wasa Taro", now); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUserIcon(ctx, "hmac-key", "data:image/png;base64,AA=="); err != nil {
		t.Fatal(err)
	}
	profile, ok, err := store.GetUserProfile(ctx, "hmac-key")
	if err != nil || !ok || profile.Icon == "" {
		t.Fatalf("利用者画像を読み戻せない: ok=%v err=%v profile=%+v", ok, err, profile)
	}
	// ログインのたびに名前と最終利用を更新しても、本人が設定した画像は消さない。
	if err := store.SaveUserProfile(ctx, "hmac-key", "42 Wasa Taro", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	profile, _, _ = store.GetUserProfile(ctx, "hmac-key")
	if profile.Icon == "" {
		t.Fatal("プロフィール更新で利用者画像が消えた")
	}
	if err := store.SaveUserIcon(ctx, "hmac-key", ""); err != nil {
		t.Fatal(err)
	}
	profile, _, _ = store.GetUserProfile(ctx, "hmac-key")
	if profile.Icon != "" {
		t.Fatal("利用者画像を外せていない")
	}
}

func TestMemoryCountsActualAPIRequestsAndPurgesAuditLogs(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	for range 3 {
		reserved, err := store.ReserveAPIRequest(ctx, "2026-08-11", "gemini-test", 500, now)
		if err != nil || !reserved {
			t.Fatalf("API試行を予約できない: reserved=%v err=%v", reserved, err)
		}
	}
	usage, _ := store.ListAPIUsage(ctx, "2026-08-11")
	if len(usage) != 1 || usage[0].Requests != 3 {
		t.Fatalf("実リクエストを数えられていない: %+v", usage)
	}
	_ = store.SaveUsageEvent(ctx, UsageEvent{ID: "old", OccurredAt: now.Add(-time.Hour)})
	_ = store.SaveUsageEvent(ctx, UsageEvent{ID: "border", OccurredAt: now})
	if removed, _ := store.PurgeUsageEvents(ctx, now); removed != 1 {
		t.Fatalf("古い利用ログだけを消していない: %d", removed)
	}
	_ = store.SaveAdminAudit(ctx, AdminAudit{ID: "old", OccurredAt: now.Add(-time.Hour)})
	_ = store.SaveAdminAudit(ctx, AdminAudit{ID: "border", OccurredAt: now})
	if removed, _ := store.PurgeAdminAudits(ctx, now); removed != 1 {
		t.Fatalf("古い管理者ログだけを消していない: %d", removed)
	}
}

func TestMemoryKeepsNewestThirtyChats(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	for i := range 31 {
		chat := Chat{
			ID: fmt.Sprintf("chat-%02d", i), UpdatedAt: fmt.Sprintf("2026-08-08T00:%02d:00Z", i),
		}
		if err := store.SaveChat(ctx, "利用者", chat, 30); err != nil {
			t.Fatal(err)
		}
	}
	chats, err := store.ListChats(ctx, "利用者", 30)
	if err != nil || len(chats) != 30 {
		t.Fatalf("30件に整理されていない: len=%d err=%v", len(chats), err)
	}
	if chats[0].ID != "chat-30" || chats[len(chats)-1].ID != "chat-01" {
		t.Fatalf("新しい30件が残っていない: first=%s last=%s", chats[0].ID, chats[len(chats)-1].ID)
	}
}

// プライバシーポリシーに「1年で削除する」と書いた以上、期限の判定が
// 実際に効いていることを固定しておく。境界（ちょうど期限の値）は消さない。
func TestMemoryPurgesFeedbackOlderThanCutoff(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	items := map[string]string{
		"古い":  "2025-08-08T23:59:59Z",
		"境界":  "2025-08-09T00:00:00Z",
		"新しい": "2026-08-09T00:00:00Z",
	}
	for id, at := range items {
		if err := store.SaveFeedback(ctx, Feedback{ID: id, Kind: "general", SubmittedAt: at}); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := store.PurgeFeedback(ctx, "2025-08-09T00:00:00Z")
	if err != nil || removed != 1 {
		t.Fatalf("期限より古い1件だけを消していない: removed=%d err=%v", removed, err)
	}

	list, err := store.ListFeedback(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("残る件数が違う: %d", len(list))
	}
	for _, item := range list {
		if item.ID == "古い" {
			t.Fatal("期限を過ぎた報告が残っている")
		}
	}
}

func TestMemoryListsAllFeedbackWhenLimitIsZero(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	for _, id := range []string{"1", "2", "3"} {
		if err := store.SaveFeedback(ctx, Feedback{ID: id, SubmittedAt: id}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.ListFeedback(ctx, 0)
	if err != nil || len(items) != 3 {
		t.Fatalf("全件を書き出せない: count=%d err=%v", len(items), err)
	}
}

// Firestoreは保存時と読出時に値を直列化するため、呼び出し側のスライスとは
// 共有されない。Memoryも同じ振る舞いでなければ、テストだけ権限が後から変わる。
func TestMemoryDoesNotShareAdminRoleSlices(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	role := AdminRole{Username: "利用者", Tools: []string{"drive"}}
	if err := store.SaveAdminRole(ctx, "key", role); err != nil {
		t.Fatal(err)
	}

	role.Tools[0] = "書換済み"
	saved, ok, err := store.GetAdminRole(ctx, "key")
	if err != nil || !ok || len(saved.Tools) != 1 || saved.Tools[0] != "drive" {
		t.Fatalf("保存後の入力変更が内部状態へ漏れた: ok=%v role=%+v err=%v", ok, saved, err)
	}

	saved.Tools[0] = "再書換済み"
	again, _, _ := store.GetAdminRole(ctx, "key")
	if len(again.Tools) != 1 || again.Tools[0] != "drive" {
		t.Fatalf("読出値の変更が内部状態へ漏れた: %+v", again)
	}
}

func TestMemoryReservesAPIRequestsAtomically(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	const limit = 5
	const workers = 50

	var wg sync.WaitGroup
	accepted := make(chan bool, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.ReserveAPIRequest(ctx, "2026-09-13", "gemini-test", limit, time.Now().UTC())
			if err != nil {
				t.Errorf("予約に失敗: %v", err)
			}
			accepted <- ok
		}()
	}
	wg.Wait()
	close(accepted)

	count := 0
	for ok := range accepted {
		if ok {
			count++
		}
	}
	if count != limit {
		t.Fatalf("上限を原子的に守れていない: accepted=%d want=%d", count, limit)
	}
	usage, err := store.ListAPIUsage(ctx, "2026-09-13")
	if err != nil || len(usage) != 1 || usage[0].Requests != limit {
		t.Fatalf("予約数の記録が不正: usage=%+v err=%v", usage, err)
	}
}

func TestMemoryUpdateAssistantRequiresExistingAssistant(t *testing.T) {
	store := NewMemory()
	err := store.UpdateAssistant(context.Background(), Assistant{ID: "missing", Name: "存在しない"})
	if !errors.Is(err, ErrAssistantNotFound) {
		t.Fatalf("存在しないアシスタントを新規作成した: %v", err)
	}
}
