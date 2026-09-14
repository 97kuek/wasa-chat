package index

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ⚠️ **索引を作る側（Python）と読む側（Go）で出所の文字列がずれると、
// そのページは「知らない出所」として黙って読まれなくなる。**
//
// 2026-09-13、共有ドライブを足したときに Go 側の一覧を1か所だけ更新し忘れ、
// Wikiを指定した質問へDriveの資料が混ざる状態になっていた。同じ壊れ方を
// 繰り返さないよう、ingest/build_index.py の実際の呼び出しと突き合わせる。
func TestOriginsMatchBuildIndex(t *testing.T) {
	root := repoRoot(t)
	source, err := os.ReadFile(filepath.Join(root, indexBuilder))
	if err != nil {
		// ⚠️ **Skip にすると、見つからなくなったことに誰も気づかない。**
		// この検査は「出所のずれ」を見張るためのもので、取り込み側を
		// 動かしたときこそ効いてほしい（2026-09-14に ingest/ へ移した）
		t.Fatalf("%s を読めません: %v", indexBuilder, err)
	}

	// load_external_pages(SITE_DUMP, "site", ...) の第2引数を拾う
	pattern := regexp.MustCompile(`load_external_pages\([A-Z_]+,\s*"([a-z]+)"`)
	found := map[string]bool{OriginWiki: true} // Wikiは外部取り込みではないので常にある
	for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
		found[match[1]] = true
	}
	if len(found) < 2 {
		t.Fatalf("%s から出所を読み取れませんでした: %v", indexBuilder, found)
	}

	for _, origin := range Origins {
		if !found[origin] {
			t.Errorf("Goは %q を索引の出所として扱うが、%s が作っていない", origin, indexBuilder)
		}
		delete(found, origin)
	}
	for origin := range found {
		t.Errorf("%s が %q を作るが、Goの Origins に無い（黙って読まれなくなる）", indexBuilder, origin)
	}
}

// indexBuilder は索引を作るスクリプトの、リポジトリ直下からの位置。
// **ここ1か所だけを直せば追随できるようにする。**
const indexBuilder = "ingest/build_index.py"

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, indexBuilder)); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("リポジトリの根を見つけられません")
	return ""
}

// 索引の出所と、アシスタントが選べる範囲は別。共有ドライブは「+」で足すもので、
// アシスタントの選択肢には出さない
func TestOriginsAreLowercaseAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, origin := range Origins {
		if origin != strings.ToLower(origin) {
			t.Errorf("出所は小文字にすること: %q", origin)
		}
		if seen[origin] {
			t.Errorf("重複している: %q", origin)
		}
		seen[origin] = true
	}
}
