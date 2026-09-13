package server

import (
	"context"
	"testing"

	"github.com/97kuek/wasa-chat/backend/internal/state"
)

func TestUserKeyDoesNotExposeUsername(t *testing.T) {
	srv := &Server{cfg: Config{SessionSecret: "テスト用の固定鍵"}}
	first := srv.userKey("利用者A")
	if first != srv.userKey("利用者A") || first == srv.userKey("利用者B") {
		t.Fatal("利用者キーが利用者ごとに安定していない")
	}
	if first == "利用者A" {
		t.Fatal("利用者名をそのまま保存キーにしている")
	}
}

// 返却が必要になる場面の多くは利用者の切断であり、その時点で
// リクエストのcontextはキャンセル済みになっている。メモリ実装は
// contextを見ないので素通りするが、Firestoreはトランザクションを
// 開始できず返却が黙って失敗する。切り離せていることを直接確かめる。
func TestRefundSurvivesCancelledRequestContext(t *testing.T) {
	shared := &contextRecordingStore{Memory: state.NewMemory()}
	srv := &Server{cfg: Config{SessionSecret: "テスト用の固定鍵テスト用の固定鍵", DailyLimit: 30}, state: shared}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if err := srv.refund(cancelled, "利用者キー", today()); err != nil {
		t.Fatalf("キャンセル済みcontextで返却が失敗した: %v", err)
	}
	if shared.refundErr != nil {
		t.Errorf("返却へ渡したcontextがキャンセル済みだった: %v", shared.refundErr)
	}
}

// Discordの回答処理はHTTP応答後も続き、期限切れで失敗することがある。
// その時点のcontextを返却へ再利用するとFirestoreだけ利用回数が戻らない。
func TestDiscordRefundSurvivesCancelledJobContext(t *testing.T) {
	shared := &contextRecordingStore{Memory: state.NewMemory()}
	srv := &Server{cfg: Config{SessionSecret: "テスト用の固定鍵テスト用の固定鍵", DailyLimit: 30}, state: shared}

	ctx, cancel := context.WithCancel(context.Background())
	refund, message := srv.takeDiscordQuota(ctx, "discord-user")
	if refund == nil {
		t.Fatalf("利用回数を確保できない: %s", message)
	}
	cancel()
	refund()

	if shared.refundErr != nil {
		t.Errorf("Discordの返却へ期限切れcontextを渡した: %v", shared.refundErr)
	}
}

func TestAdminAuditSurvivesCancelledRequestContext(t *testing.T) {
	shared := &contextRecordingStore{Memory: state.NewMemory()}
	srv := &Server{cfg: Config{SessionSecret: "テスト用の固定鍵テスト用の固定鍵"}, state: shared}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	srv.saveAdminAudit(ctx, "管理者", "admin.test", "対象")
	if shared.auditErr != nil {
		t.Errorf("監査ログへキャンセル済みcontextを渡した: %v", shared.auditErr)
	}
}

// 渡されたcontextが生きているかを記録するだけのStore。
type contextRecordingStore struct {
	*state.Memory
	refundErr error
	auditErr  error
}

func (c *contextRecordingStore) Refund(ctx context.Context, user, day string) error {
	c.refundErr = ctx.Err()
	return c.Memory.Refund(ctx, user, day)
}

func (c *contextRecordingStore) SaveAdminAudit(ctx context.Context, audit state.AdminAudit) error {
	c.auditErr = ctx.Err()
	return c.Memory.SaveAdminAudit(ctx, audit)
}
