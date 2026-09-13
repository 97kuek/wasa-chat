package discord

import (
	"sort"
	"strings"
	"unicode"
)

// minTermRunes はこれ未満の塊を語として使わない。1文字は当たりすぎる。
const minTermRunes = 2

// MaxQueries は1回の質問で投げる検索の数。
//
// ⚠️ **1つのクエリに語を並べない（AND条件で当たらなくなる）。代わりに
// クエリを分けて束ねる。** 1語だけだと拾える範囲が狭く、「Discordを見ている
// 感じがしない」という指摘が出た（2026-09-13）。語ごとに別々に探せば、
// AND で潰さずに広く拾える。検索は1回1リクエストなので、3回でも軽い。
const MaxQueries = 3

// followUpRunes はこれ以下の質問を「前の話の続き」とみなす長さ。
//
// 「最近のは？」「他には？」のような短い質問は、**それ自体では何を探すか
// 決まらない**。前の質問の語を使ったほうが当たる（2026-09-13に本番で、
// 「荷重試験の計画書ってある？」→「最近のは？」が別の話になった）。
const followUpRunes = 12

// verbStems は、送り仮名の前に立つ1文字の動詞。
//
// 「書かれていますか」は「書」＋ひらがなに割れるので、1文字の候補として
// 残ってしまう。**これは探す語ではない。** 「桁」「翼」のような1文字の部材名と
// 区別するために、動詞のほうを名前で挙げる（品詞を判定する仕組みは入れない）。
var verbStems = map[string]bool{
	"書": true, "教": true, "知": true, "見": true, "使": true, "作": true,
	"決": true, "言": true, "思": true, "出": true, "入": true, "来": true,
	"行": true, "分": true, "聞": true, "読": true, "持": true, "取": true,
}

// 質問の骨組みを作る語。**これ自体は探す対象ではない。**
// 「どんな内容が書かれていますか」を検索しても何も当たらない。
var queryStopWords = map[string]bool{
	"discord": true, "ディスコード": true, "チャット": true, "wasa": true,
	"教えて": true, "教えてください": true, "知りたい": true, "ください": true,
	"どんな": true, "どこ": true, "なに": true, "なん": true, "いつ": true, "だれ": true,
	"内容": true, "こと": true, "もの": true, "ため": true, "とき": true, "ところ": true,
	"方法": true, "場合": true, "色々": true, "いろいろ": true, "全部": true,
	"書かれて": true, "書いて": true, "あります": true, "ますか": true, "ですか": true,
}

// SearchTermsFor は、直前の質問も踏まえて検索語を**複数**返す。
//
// ⚠️ **1つのクエリにまとめない。** Discordの content は複数語をAND条件で
// 扱うため、「荷重試験 申請」は21件が1件になる（docs/08 M64）。
// 別々のクエリとして投げ、結果を束ねる。
//
// 並びは特徴的な順（長い順）。前の質問の語は、短い追加質問のときだけ足す。
func SearchTermsFor(question, previous string) []string {
	terms := searchTerms(question)
	if previous != "" && len([]rune(strings.TrimSpace(question))) <= followUpRunes {
		// 「最近のは？」のような短い質問は、それ自体では何を探すか決まらない。
		// 前の質問の語を**先頭に**置く（そちらが本題）
		terms = append(searchTerms(previous), terms...)
	}
	seen := map[string]bool{}
	out := make([]string, 0, MaxQueries)
	for _, term := range terms {
		if term == "" || seen[term] {
			continue
		}
		seen[term] = true
		if out = append(out, term); len(out) == MaxQueries {
			break
		}
	}
	return out
}

// searchTerms は質問から語を、特徴的な順（長い順）に取り出す。
func searchTerms(question string) []string {
	var terms, weak []string
	for _, chunk := range splitQuestion(question) {
		if queryStopWords[strings.ToLower(chunk)] {
			continue
		}
		if len([]rune(chunk)) < minTermRunes || allDigits(chunk) {
			// 1文字・数字だけは当たりすぎる。ほかに候補が無いときだけ使う
			if !verbStems[chunk] {
				weak = append(weak, chunk)
			}
			continue
		}
		terms = append(terms, chunk)
	}
	sort.SliceStable(terms, func(a, b int) bool {
		return len([]rune(terms[a])) > len([]rune(terms[b]))
	})
	return append(terms, weak...)
}

// SearchQueryFor は、直前の質問も踏まえて検索語を選ぶ。
//
// 短い質問（「最近のは？」）は指示語だけで、そのまま語を取ると「最近」のような
// 当たらない語になる。前の質問のほうが具体的ならそちらを使う。
//
// **長い質問では前の質問を見ない。** 話題が変わったときに引きずると、
// 関係のない会話を根拠として渡すことになる。
func SearchQueryFor(question, previous string) string {
	term := SearchQuery(question)
	if previous == "" || len([]rune(strings.TrimSpace(question))) > followUpRunes {
		return term
	}
	if earlier := SearchQuery(previous); len([]rune(earlier)) > len([]rune(term)) {
		return earlier
	}
	return term
}

// SearchQuery は質問から、Discordの検索に渡す語を1つ選ぶ。空なら検索しない。
//
// ⚠️ **質問文をそのまま渡さない。** Discordの `content` は語の一致で探すので、
// 文をまるごと渡すと必ず0件になる。実際にそれで「Discordを見ています」と
// 言いながら1件も読んでいなかった（2026-09-13に本番で発覚）。
//
// ⚠️ **語は1つだけにする。** 複数渡すとAND条件になり、急に当たらなくなる。
// 同じサーバーで実測した値（2026-09-13、docs/08 M64）:
//
//	申請 16件       申請方法 1件
//	荷重試験 21件    「荷重試験 申請」1件
//	チャンネル 7件   「チャンネル 名」0件
//
// 前後の文脈は `around` で取り、絞り込みはモデルに任せる。ここでは
// **取りこぼさないこと**を優先する。
//
// 選ぶのは「いちばん長い塊」。長い語ほどその質問に固有で、短い語は
// 助数詞や一般語になりやすい。形態素解析は入れない（辞書と依存が増えるわりに、
// ここで要るのは助詞と疑問文の型を落とす程度）。**足りなければ測ってから足す。**
func SearchQuery(question string) string {
	best, fallback := "", ""
	for _, chunk := range splitQuestion(question) {
		if queryStopWords[strings.ToLower(chunk)] {
			continue
		}
		// 数字だけの塊（「40代」の 40、年号）は単独では当たりすぎる。
		// 「40代の代表は？」で "40" ではなく "代表" を選びたい
		if len([]rune(chunk)) < minTermRunes || allDigits(chunk) {
			// 1文字は当たりすぎるので普通は使わない。ただし「桁」「翼」のように
			// **1文字の部材名**もあるため、他に候補が無ければ使う。
			// 動詞の語幹（「書」かれて、「教」えて）はここで落とす
			if fallback == "" && !verbStems[chunk] {
				fallback = chunk
			}
			continue
		}
		if len([]rune(chunk)) > len([]rune(best)) {
			best = chunk
		}
	}
	if best == "" {
		return fallback
	}
	return best
}

// splitQuestion は質問を、文字の種類が変わるところで区切る。
//
// 日本語は空白で区切られないので、**ひらがなの連なりを境目として使う**。
// 「翼型の設計はどこ」→「翼型」「の」「設計」「はどこ」のように割れ、
// ひらがなだけの塊を捨てれば名詞が残る。助詞を辞書で持つより壊れにくい。
// allDigits は数字だけの塊かを返す。
func allDigits(chunk string) bool {
	for _, r := range chunk {
		if r < '0' || r > '9' {
			return false
		}
	}
	return chunk != ""
}

func splitQuestion(question string) []string {
	var chunks []string
	var current strings.Builder
	var currentKind int

	flush := func() {
		if current.Len() > 0 {
			chunks = append(chunks, current.String())
			current.Reset()
		}
	}
	for _, r := range question {
		kind := runeKind(r)
		if kind == kindSkip {
			flush()
			currentKind = kindSkip
			continue
		}
		if kind != currentKind {
			flush()
			currentKind = kind
		}
		current.WriteRune(r)
	}
	flush()

	// ひらがなだけの塊は助詞・語尾なので落とす
	out := chunks[:0]
	for _, chunk := range chunks {
		if runeKind([]rune(chunk)[0]) != kindHiragana {
			out = append(out, chunk)
		}
	}
	return out
}

const (
	kindSkip = iota
	kindHiragana
	kindKatakana
	kindKanji
	kindASCII
)

func runeKind(r rune) int {
	switch {
	case unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r):
		return kindSkip
	case unicode.In(r, unicode.Hiragana):
		return kindHiragana
	case unicode.In(r, unicode.Katakana) || r == 'ー':
		return kindKatakana
	case unicode.In(r, unicode.Han):
		return kindKanji
	default:
		return kindASCII
	}
}
