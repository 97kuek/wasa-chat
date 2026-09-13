package pipeline

import "testing"

// ⚠️ **特定の設問に合わせた処理ではない。** どの語にも同じ規則をかける。
// 実データでの出現回数は docs/08 M68 にある（シミュレータ19/シミュレーター33 など）。
func TestNormalize(t *testing.T) {
	cases := []struct{ in, want, why string }{
		{"シミュレーター", "シミュレータ", "語末の長音を落とす"},
		{"モーター", "モータ", "語末の長音を落とす"},
		{"サーバー", "サーバ", "語末の長音を落とす"},
		// **語中の長音は残す。** 落とすと別の語になる
		{"コーヒー", "コーヒ", "語中のーは残し、語末だけ落とす"},
		{"データベース", "データベース", "語中のーは残す（後ろがカタカナなら語末ではない）"},
		{"TR-797", "tr797", "型番の中の区切りを落とす"},
		{"DAE-51", "dae51", "型番の中の区切りを落とす"},
		{"ＴＲ７９７", "tr797", "全角を半角にする"},
		{"１２３", "123", "全角数字を半角にする"},
		// **語と語の間のハイフンは残す。** 英単語の複合語を壊さない
		{"drag-free", "drag-free", "英単語の複合語は壊さない"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := Normalize(c.in); got != c.want {
				t.Fatalf("%q → %q（期待 %q）: %s", c.in, got, c.want, c.why)
			}
		})
	}
}

// **質問と本文の両方に同じ処理をかける。** 片方だけだと当たらない
func TestNormalizeMatchesBothSides(t *testing.T) {
	pairs := [][2]string{
		{"フライトシミュレーターの使い方", "フライトシミュレータの起動手順"},
		{"TR-797とは", "TR797法について"},
		{"モーターの選定", "モータの選定"},
	}
	for _, pair := range pairs {
		question, document := Normalize(pair[0]), Normalize(pair[1])
		// 正規化後に共通の2文字が増えることを確かめる（bigramで当たる）
		shared := 0
		runes := []rune(question)
		for i := 0; i+1 < len(runes); i++ {
			if contains(document, string(runes[i:i+2])) {
				shared++
			}
		}
		raw := 0
		rawRunes := []rune(pair[0])
		for i := 0; i+1 < len(rawRunes); i++ {
			if contains(pair[1], string(rawRunes[i:i+2])) {
				raw++
			}
		}
		if shared <= raw {
			t.Fatalf("%q と %q で当たりが増えていない（%d → %d）", pair[0], pair[1], raw, shared)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// 型番は区切りを全部落とす（Normalize より強い）
func TestNormalizeIdentifier(t *testing.T) {
	for _, c := range [][2]string{
		{"TR-797", "tr797"}, {"TR797", "tr797"},
		{"DAE-51", "dae51"}, {"ESP_32", "esp32"},
	} {
		if got := normalizeIdentifier(c[0]); got != c[1] {
			t.Fatalf("%q → %q（期待 %q）", c[0], got, c[1])
		}
	}
}
