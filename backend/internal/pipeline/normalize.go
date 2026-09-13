package pipeline

import (
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// 表記ゆれを均す。**質問と本文の両方に同じ処理をかけること。**
//
// 部内資料は複数人が書くので、同じ語が違う書き方で入る。実データで測った
// （2026-09-13、docs/08 M68）:
//
//	シミュレータ 19回 / シミュレーター 33回
//	モータ 57回 / モーター 33回
//	サーバ 2回 / サーバー 21回
//	TR797 5回 / TR-797 10回
//	全角の「１」909回、「Ａ」172回
//
// ⚠️ **特定の設問に合わせた処理ではない。** どの語にも同じ規則をかける。
// 評価ハーネス（eval/retrieval_eval.py の normalize）と同じ規則にすること。
// ずれると、評価で良くなった改善が本番で効かない。

// identifierSeparator は型番の中に入る区切り（tr-797 → tr797）。
// **語と語の間のハイフンは残す。** 英単語の複合語を壊さないため、
// 前が英数字・後ろが数字のときだけ落とす。
var identifierSeparator = regexp.MustCompile(`([0-9a-z])[-_]([0-9])`)

// trailingChoon はカタカナ語末の長音（シミュレーター → シミュレータ）。
//
// **語中の長音は残す。**「データベース」の最初のーを落とすと別の語になる。
// 後ろがカタカナでないときだけ落とす。
//
// ⚠️ **eval/retrieval_eval.py の normalize と同じ規則にすること。**
// ずれると、評価で良くなった改善が本番で効かない（eval/test_parity.py が見張る）。
var trailingChoon = regexp.MustCompile(`ー([^ァ-ヶ]|$)`)

// Normalize は検索の前に表記ゆれを均す。
func Normalize(text string) string {
	text = strings.ToLower(norm.NFKC.String(text))
	text = identifierSeparator.ReplaceAllString(text, "$1$2")
	return trailingChoon.ReplaceAllString(text, "$1")
}
