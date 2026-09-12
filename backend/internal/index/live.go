package index

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"cloud.google.com/go/storage"
)

// Live は動いたまま差し替えられる索引。
//
// # なぜ要るのか
//
// 索引は起動時にメモリへ読む作りで、差し替えたあとは**環境変数を1つ動かして
// 新しいリビジョンへ入れ替える**必要があった（docs/07）。つまり資料を直しても、
// 動いているインスタンスは気づかない。ここが「更新が反映されるまでの待ち」の正体で、
// リアルタイム更新にするには最初に外さないといけない仕掛けだった。
//
// # 1回の質問の中では索引を替えない
//
// ⚠️ **`Current()` の結果を、質問の途中で取り直さないこと。**
// 節IDは `p{ページ}-c{連番}` で、索引を作り直すとずれ得る（M51）。
// ページを選んだ索引と節を読む索引が違うと、**選んだはずの節と別の本文を
// 渡す**ことになる。呼び出し側は質問の入口で1回だけ取り、以降はそれを持ち回る。
//
// 差し替えは atomic.Pointer の入れ替えだけで、読み手を待たせない。
// 古い索引は、それを掴んでいる質問が終わればGCが回収する。
type Live struct {
	current atomic.Pointer[Index]
	// location は gs://… か、手元のディレクトリ
	location string
	// stamp は「いま読んでいる版」の印。GCSなら世代番号、ファイルなら更新時刻。
	// **中身を比べない。** 3.4MBを毎回読んで比較すると、確認そのものが重い
	stamp atomic.Value
}

// NewLive は読み込み済みの索引から始める。location は Reload の取得元。
func NewLive(ix *Index, location string) *Live {
	live := &Live{location: location}
	live.current.Store(ix)
	live.stamp.Store("")
	return live
}

// Current はいま使うべき索引を返す。質問の入口で1回だけ呼ぶこと。
func (l *Live) Current() *Index { return l.current.Load() }

// Location は取得元を返す（管理画面の表示用）。
func (l *Live) Location() string { return l.location }

// Stamp はいま読んでいる版の印を返す。空なら一度も確認していない。
func (l *Live) Stamp() string {
	stamp, _ := l.stamp.Load().(string)
	return stamp
}

// Reload は取得元が変わっていれば読み直す。変えたかどうかを返す。
//
// **変わっていなければ何も読まない。** 印（GCSの世代 / ファイルの更新時刻）だけを
// 見るので、確認は1リクエストで済む。
func (l *Live) Reload(ctx context.Context) (bool, error) {
	stamp, err := l.stampOf(ctx)
	if err != nil {
		return false, err
	}
	if stamp != "" && stamp == l.Stamp() {
		return false, nil
	}

	next, err := l.load(ctx)
	if err != nil {
		return false, err
	}
	// **読めたものだけを差し替える。** 途中で失敗したときに空の索引を載せると、
	// 全部の質問へ「資料が見つかりません」と答え続ける状態になる
	l.current.Store(next)
	l.stamp.Store(stamp)
	return true, nil
}

func (l *Live) load(ctx context.Context) (*Index, error) {
	if _, _, ok := ParseGCS(l.location); ok {
		return LoadGCS(ctx, l.location)
	}
	return Load(l.location)
}

// stampOf は「版が変わったか」を判断する印を取る。取れなければ空を返す。
func (l *Live) stampOf(ctx context.Context) (string, error) {
	bucket, prefix, ok := ParseGCS(l.location)
	if !ok {
		// 手元とdocker compose。index.json の更新時刻で足りる
		info, err := os.Stat(filepath.Join(l.location, "index.json"))
		if err != nil {
			return "", fmt.Errorf("索引の更新時刻を読めません: %w", err)
		}
		return info.ModTime().UTC().Format(time.RFC3339Nano), nil
	}

	client, err := storage.NewClient(ctx)
	if err != nil {
		return "", fmt.Errorf("Cloud Storage へ接続: %w", err)
	}
	defer client.Close()

	attrs, err := client.Bucket(bucket).Object(prefix + "index.json").Attrs(ctx)
	if err != nil {
		return "", fmt.Errorf("索引の世代を読めません: %w", err)
	}
	// 世代番号は差し替えのたびに必ず変わる。更新時刻より確実
	return fmt.Sprintf("%d", attrs.Generation), nil
}

// Watch は every ごとに取得元を見て、変わっていれば読み直す。
//
// ⚠️ **失敗しても止めない。** 取得元が一時的に読めなくても、いま持っている索引で
// 答え続けるほうが、資料なしで答えるより良い。次の周回で復帰する。
//
// min-instances=0 なので、使われていない間はこのループごと止まる。
// **それでよい。** 次のアクセスで起動したインスタンスは最新を読むので、
// 「誰も使っていない時間の更新を取り込み損ねる」ことは起きない。
func (l *Live) Watch(ctx context.Context, every time.Duration, onChange func(*Index)) {
	if every <= 0 {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			changed, err := l.Reload(ctx)
			if err != nil {
				log.Printf("索引の更新確認に失敗（いまの索引で続けます）: %v", err)
				continue
			}
			if !changed {
				continue
			}
			ix := l.Current()
			pages, chunks := ix.Stats()
			log.Printf("索引を読み直しました: %dページ / %dチャンク（版 %s）", pages, chunks, l.Stamp())
			if onChange != nil {
				onChange(ix)
			}
		}
	}
}
