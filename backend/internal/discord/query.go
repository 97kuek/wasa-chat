package discord

import (
	"strings"
	"unicode"
)

// minTermRunes はこれ未満の塊を語として使わない。1文字は当たりすぎる。
const minTermRunes = 2

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
