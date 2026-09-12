package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 索引を差し替えたら、再デプロイせずに取り込めること。
//
// 以前は起動時に1回読むだけで、差し替え後は環境変数を動かして新しいリビジョンへ
// 入れ替える必要があった（docs/07）。資料を直してから反映されるまでの待ちは、
// ここが作っていた。
func TestLiveReloadPicksUpNewIndex(t *testing.T) {
	dir := t.TempDir()
	write := func(title string) {
		t.Helper()
		body := `{"pages":[{"id":"1","source":"wiki","title":"` + title +
			`","url":"https://example/x","last_edited":"2026-04-01","chars":10,` +
			`"chunks":[{"id":"p1-c0","breadcrumb":"` + title + `","text":"本文","chars":2}]}]}`
		if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "toc.md"), []byte("# 目次\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("古いページ")
	first, err := Load(dir)
	if err != nil {
		t.Fatalf("最初の読み込みに失敗: %v", err)
	}
	live := NewLive(first, dir)

	// **起動直後は読み直さない。** NewLive が版を控えるので、変わっていなければ
	// 3.4MBを読むことはない（2026-09-12にCodexが指摘）
	if changed, err := live.Reload(context.Background()); err != nil || changed {
		t.Fatalf("起動直後に読み直した: changed=%v err=%v", changed, err)
	}
	if status := live.Status(); status.Failures != 0 || status.Stamp == "" {
		t.Fatalf("起動直後の状態がおかしい: %+v", status)
	}

	// 更新時刻で判定するため、同一秒内の書き換えを避ける
	time.Sleep(10 * time.Millisecond)
	write("新しいページ")
	if changed, err := live.Reload(context.Background()); err != nil || !changed {
		t.Fatalf("差し替えたのに読み直していない: changed=%v err=%v", changed, err)
	}
	if _, ok := live.Current().Resolve("新しいページ"); !ok {
		t.Fatal("読み直した索引に新しいページが無い")
	}
	if _, ok := live.Current().Resolve("古いページ"); ok {
		t.Fatal("古い索引が残っている")
	}
}

// **読めなかったときは、いま持っている索引で答え続ける。**
// 空の索引を載せると、全部の質問へ「資料が見つかりません」と答え続ける状態になる。
func TestLiveKeepsIndexWhenReloadFails(t *testing.T) {
	dir := t.TempDir()
	ix := &Index{Version: "もとの版"}
	live := NewLive(ix, dir)

	if _, err := live.Reload(context.Background()); err == nil {
		t.Fatal("索引が無いのに成功した")
	}
	if live.Current() != ix {
		t.Fatal("読み込みに失敗したのに索引を差し替えた")
	}
	// **失敗を隠さない。** 起動時は読めなければ止めるのに、起動後は黙って古いまま
	// 動き続ける、という非対称を残さない（docs/09 B-2）
	status := live.Status()
	if status.Failures == 0 || status.LastError == "" {
		t.Fatalf("失敗が記録されていない: %+v", status)
	}
}
