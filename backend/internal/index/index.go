// Package index は build_index.py が生成した index.json / toc.md を読み込み、
// ページとチャンクの参照を提供する。
//
// データベースは使わない。114ページ・348チャンク・899KB であり、
// 全件をメモリに載せて総当たりする方が速く、運用も単純になる（docs/01-設計方針.md §7）。
package index

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var aliasSuffix = regexp.MustCompile(`\s*（別名:[^）]*）\s*$`)

// Era は本文が「いつの話か」の手がかり。build_index.py の extract_era が入れる。
//
// ページの LastEdited は編集した日であって、書かれている内容の新しさではない。
// 実測では911チャンク中155件（17%）で、本文が扱う年代が最終更新より2年以上古かった。
// 「鳥コン > 鳥コンまでの流れ」は最終更新2026年だが本文は2024年までしか書いていない。
type Era struct {
	Years []int `json:"years"` // 本文中の西暦（代から換算したものを含む）
	Gens  []int `json:"gens"`  // 本文中の代
}

// Label はプロンプトに載せる短い表記を返す。手がかりが無ければ空文字。
func (e Era) Label() string {
	if len(e.Years) == 0 {
		return ""
	}
	lo, hi := e.Years[0], e.Years[len(e.Years)-1]
	if lo == hi {
		return fmt.Sprintf("%d年", hi)
	}
	return fmt.Sprintf("%d〜%d年（最新%d年）", lo, hi, hi)
}

// Chunk は検索・回答の単位。breadcrumb は「ページ名 > 見出し > 見出し」。
type Chunk struct {
	ID         string `json:"id"`
	Breadcrumb string `json:"breadcrumb"`
	Text       string `json:"text"`
	Chars      int    `json:"chars"`
	Era        Era    `json:"era"`
}

type Page struct {
	// IDは文字列。Wikiのページは pageid だが、公式サイトのページは "s1" 形式で
	// 数値ではない。出所が2つある以上、数値型には固定できない
	ID string `json:"id"`
	// Revid はWikiの更新確認に使う。本文検索には不要だが、管理画面から
	// 公開元だけを軽く照合するため、索引へ既に入っている値を保持する。
	Revid      int      `json:"revid,omitempty"`
	Title      string   `json:"title"`
	Aliases    []string `json:"aliases"`
	URL        string   `json:"url"`
	LastEdited string   `json:"last_edited"`
	Team       string   `json:"team"`
	Source     string   `json:"source"` // "wiki" | "site" | "fee"。出典表示で出所を区別する
	Gen        *int     `json:"gen"`
	Chars      int      `json:"chars"`
	IsStub     bool     `json:"is_stub"`
	Chunks     []Chunk  `json:"chunks"`
}

type Index struct {
	// Versionは本文を外へ出さず、どの索引を読んでいるか照合するための短いハッシュ。
	// 管理画面で「公開した索引へ本当に切り替わったか」を判断するために使う。
	Version string
	TOC     string // 常時コンテキストに載せる目次（共有ドライブの節は含まない）
	// DriveTOC は共有ドライブの節だけ。**「+」でオンにしたときだけ載せる。**
	// 固定プレフィックスへ混ぜるとキャッシュが外れ、オフの会話へも漏れる
	DriveTOC string
	// SiteTOC は公式サイト（一般公開）の部分だけを抜いた目次。
	//
	// 「公式サイトのみ」のアシスタントに TOC をそのまま渡すと、選択ページと
	// 出典は公式サイトだけなのに、**回答本文は引き継ぎWikiのページ名や
	// リード文を目次から拾えてしまう**（回答プロンプトが目次を根拠にしてよいと
	// 明記しているため）。部外に出せる情報だけ、という表示と食い違うので、
	// 出所を絞ったときは目次も差し替える。
	//
	// キャッシュは「全体」と「公式サイトのみ」の2種類までしか分裂しない。
	// 班の絞り込みで目次を変えないのは、班は機密の境界ではなく関連度の
	// 絞り込みでしかなく、分裂させるとキャッシュが班の数だけ増えるため。
	SiteTOC string
	Pages   []Page
	byName  map[string]*Page // タイトルと別名の両方から引ける
	byID    map[string]*Chunk
	owner   map[string]*Page // チャンクID → 所属ページ
}

type file struct {
	Pages []Page `json:"pages"`
}

// 目次の出所ごとの見出し。build_toc.py が出力する文言と一致させること。
const (
	wikiHeading   = "\n## 引き継ぎWiki（部内限定）"
	siteHeading   = "\n## 公式サイト（一般公開"
	driveHeading  = "\n## 共有ドライブ（部内限定"
	originHeading = "\n## " // 出所の節の切れ目
)

// splitDriveTOC は目次から共有ドライブの節を切り離す。
//
// ⚠️ **共有ドライブの節を固定プレフィックスに入れない。** 目次はプロンプトの
// 先頭に固定してキャッシュを効かせている（docs/01 §4）。Driveの節を混ぜると、
//
//   - ファイルが1つ増減するたびに前半のバイト列まで変わり、**キャッシュが外れる**
//   - Driveをオフにしている会話へ、ファイル名と見出しが漏れる
//
// build_toc.py は共有ドライブの節を**必ず最後**に置く。ここで切って、
// オンのときだけ後ろへ足す（2026-09-13のCodex指摘）。
func splitDriveTOC(toc string) (fixed, drive string) {
	at := strings.Index(toc, driveHeading)
	if at < 0 {
		return toc, ""
	}
	return toc[:at] + "\n", strings.TrimSpace(toc[at:])
}

// siteOnlyTOC は目次から公式サイトの節だけを抜き出す。
//
// 先頭の事実カード（人が書いた団体の基本情報）と公式サイトの節だけを残す。
// **見出しが見つからないときは空を返す。** 全体を返すと部内資料が
// 「公式サイトのみ」の回答へ混ざるので、目次なしで動く方を選ぶ
// （精度は落ちるが、混ざるよりよい）。
//
// ⚠️ **公式サイト節の「終わり」も見ること。** 以前は siteAt から末尾まで
// 返していたため、後ろに足された節がそのまま付いてきた。出所が3つになった
// とき（M27でフライトシミュレータのガイドを追加）にこの関数が追随せず、
// 「公式サイトのみ」のアシスタントの目次へFEEの全32ページが入っていた
// （2026-09-12に発見）。**出所を足すたびに同じ壊れ方をしない形にする。**
func siteOnlyTOC(toc string) string {
	wikiAt := strings.Index(toc, wikiHeading)
	siteAt := strings.Index(toc, siteHeading)
	if wikiAt < 0 || siteAt < 0 || siteAt < wikiAt {
		return ""
	}
	site := toc[siteAt:]
	// 自分の見出しの次に来る `## ` までが公式サイトの節
	if next := strings.Index(site[len(originHeading):], originHeading); next >= 0 {
		site = site[:len(originHeading)+next]
	}
	return toc[:wikiAt] + "\n" + site
}

// Load は dir 直下の index.json と toc.md を読み込む。
func Load(dir string) (*Index, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return nil, fmt.Errorf("index.json の読み込み: %w", err)
	}
	toc, err := os.ReadFile(filepath.Join(dir, "toc.md"))
	if err != nil {
		return nil, fmt.Errorf("toc.md の読み込み: %w", err)
	}
	return Build(raw, toc)
}

// Build は index.json と toc.md の中身から Index を組み立てる。
//
// 読み込み元（ファイル / Cloud Storage）と組み立てを分けているのは、
// 索引の置き場所を運用側で選べるようにするためである。
func Build(raw, toc []byte) (*Index, error) {
	var parsed file
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("index.json の解析: %w", err)
	}
	if len(parsed.Pages) == 0 {
		// 空の索引で起動すると、全部の質問に「資料が見つかりません」と
		// 答え続ける。壊れていることが分からないので、ここで止める
		return nil, fmt.Errorf("index.json にページがありません")
	}

	digest := sha256.New()
	digest.Write(raw)
	digest.Write([]byte{0})
	digest.Write(toc)
	fixedTOC, driveTOC := splitDriveTOC(string(toc))
	ix := &Index{
		Version:  fmt.Sprintf("%x", digest.Sum(nil))[:12],
		TOC:      fixedTOC,
		DriveTOC: driveTOC,
		SiteTOC:  siteOnlyTOC(fixedTOC),
		Pages:    parsed.Pages,
		byName:   make(map[string]*Page),
		byID:     make(map[string]*Chunk),
		owner:    make(map[string]*Page),
	}
	for i := range ix.Pages {
		p := &ix.Pages[i]
		ix.byName[p.Title] = p
		for _, alias := range p.Aliases {
			ix.byName[alias] = p
		}
		for j := range p.Chunks {
			c := &p.Chunks[j]
			ix.byID[c.ID] = c
			ix.owner[c.ID] = p
		}
	}
	return ix, nil
}

func (ix *Index) Chunk(id string) (*Chunk, *Page, bool) {
	c, ok := ix.byID[id]
	if !ok {
		return nil, nil, false
	}
	return c, ix.owner[id], true
}

// Resolve は LLM が返したページ名を実在ページに解決する。解決できなければ false。
//
// M2a の測定で、モデルは班名（「空力」）・節名（「鳥コンまでの流れ」）・
// 命名規則から推測した架空のページ名（「構造設計 41st」）を返してきた。
// ここで落とさないと、存在しないページを根拠として回答してしまう。
func (ix *Index) Resolve(name string) (*Page, bool) {
	name = strings.TrimSpace(name)
	// 目次は「タイトル（別名: X）」と表示するため、モデルが注記ごと
	// コピーしてくることがある（M2a-gemini で2件発生）。剥がしてから照合する
	name = strings.TrimSpace(aliasSuffix.ReplaceAllString(name, ""))
	if name == "" {
		return nil, false
	}
	if p, ok := ix.byName[name]; ok {
		return p, true
	}
	// 部分一致は候補がちょうど1件に定まるときだけ採用する。
	// 「空力」のように多数にマッチする語は班名であり、ページ名ではない。
	var hit *Page
	for title, p := range ix.byName {
		if strings.Contains(title, name) {
			if hit != nil && hit != p {
				return nil, false
			}
			hit = p
		}
	}
	return hit, hit != nil
}

func (ix *Index) Stats() (pages, chunks int) {
	for i := range ix.Pages {
		chunks += len(ix.Pages[i].Chunks)
	}
	return len(ix.Pages), chunks
}
