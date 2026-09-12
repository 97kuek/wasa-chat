"""評価スクリプトそのものの検査。

なぜ評価器にテストが要るのか
-----------------------------
測定器の欠陥が4回見つかっている（M2b・M6・M13・M23）。毎回「数字が動いた →
調べたら評価器が壊れていた」という順で、そのたびに誤った数字を記録していた。

  M2b  判定器が判定基準の文をオウム返ししていた
  M6   本番と違う照合をして、実力を過小に報告していた
  M13  文書と実装の乖離
  M23  出典形式をM7で変えたのに、検出側が旧形式のままで 0% と報告していた

**原因は共通していて、プロンプトや出力形式を変えたときに、それを見ている
評価コードが置いていかれること。** 純関数だけでも固定しておけば、
形式を変えた瞬間にここが落ちて気づける。

実行:
  python -m unittest eval.test_answer_eval
"""

from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from eval.golden_check import check as check_golden  # noqa: E402
from eval.judge_calibration import cohens_kappa, confusion  # noqa: E402
from eval.answer_eval import (  # noqa: E402
    CITATION,
    citation_scores,
    retrieval_scores,
    answered_well,
    body_only,
    strip_citations,
    strip_general_knowledge,
)

# 現行の出典形式（M7で決めた）。ここを変えるならCITATIONも一緒に直す
CITATION_NOW = "- [空力設計(41st)](https://wiki.example/x)（Wiki、本文の年代: 2025年）"
# 旧形式。過去の eval/answers.json を読み直せるよう、検出は残してある
CITATION_OLD = "出典: 空力設計(41st)（最終更新: 2025-01）"


class CitationDetectionTest(unittest.TestCase):
    """出典の検出。M23で0%と誤報告した箇所。"""

    def test_detects_current_markdown_form(self) -> None:
        self.assertTrue(CITATION.search(f"結論です。\n\n{CITATION_NOW}"))

    def test_detects_legacy_form(self) -> None:
        self.assertTrue(CITATION.search(f"結論です。\n\n{CITATION_OLD}"))

    def test_does_not_fire_without_citation(self) -> None:
        self.assertFalse(CITATION.search("資料に記載がありません。"))

    def test_does_not_fire_on_inline_link(self) -> None:
        """本文中のリンクは出典ではない。行頭の箇条書きだけを出典とみなす。"""
        self.assertFalse(CITATION.search("詳しくは[この記事](https://example.com/a)が参考になります。"))

    def test_strip_removes_whole_citation_line(self) -> None:
        """出典行は主張ではないので、判定へ渡す前に行ごと落とす（M3の欠陥）。"""
        got = strip_citations(f"結論です。\n\n{CITATION_NOW}\n{CITATION_OLD}")
        self.assertEqual(got, "結論です。")

    def test_strip_removes_inline_source_numbers(self) -> None:
        """本文中の資料番号も主張ではない（測定器の欠陥・5回目）。

        Judgeへ `[1]` が付いたまま渡ると、「初めて出られなかった代は
        1993年（9代）です [1]」のような**正しい文をそのまま
        unsupported_claim へ引用**してくる。2026-08-11に確認した。
        """
        self.assertEqual(strip_citations("結論です [1]。"), "結論です。")
        self.assertEqual(strip_citations("複数の根拠があります [1][3]。"), "複数の根拠があります。")
        self.assertEqual(
            strip_citations(f"結論です [2]。\n\n{CITATION_NOW}"), "結論です。"
        )

    def test_strip_keeps_ordinary_brackets(self) -> None:
        """3桁以上の数字は資料番号ではない。年や型番を巻き込まない。"""
        self.assertEqual(strip_citations("記録[1984]を参照"), "記録[1984]を参照")


class GeneralKnowledgeTest(unittest.TestCase):
    """一般知識の節。2026-08-09にアシスタントでも許可したため、
    Faithfulness判定から外さないと正しい一般知識が「裏付けなし」になる。"""

    def test_strips_section_and_everything_after(self) -> None:
        text = (
            "スパンは32mです。\n\n"
            "## 一般知識（WASA資料外）\n\n"
            "誘導抗力は揚力係数の2乗に比例します。\n"
        )
        self.assertEqual(strip_general_knowledge(text), "スパンは32mです。")

    def test_accepts_heading_variations(self) -> None:
        for heading in (
            "## 一般知識（WASA資料外）",
            "### 一般知識（WASA資料外）",
            "**一般知識（WASA資料外）**",
            "一般知識（WASA資料外）",
        ):
            with self.subTest(heading=heading):
                self.assertEqual(
                    strip_general_knowledge(f"本文。\n\n{heading}\n\n一般論。"), "本文。"
                )

    def test_keeps_text_without_the_section(self) -> None:
        self.assertEqual(strip_general_knowledge("本文だけ。"), "本文だけ。")


class AnsweredWellTest(unittest.TestCase):
    """種別ごとの採点。M2b-2で「指示どおり答えた回答ほど誤検知される」
    構造だったことが分かり、種別で基準を分けた。"""

    LONG = "手順は次の通りです。" + "あ" * 300
    MISSING_ONLY = "資料に記載がありません。"

    def test_unanswerable_needs_short_refusal(self) -> None:
        self.assertTrue(answered_well("unanswerable", self.MISSING_ONLY))
        self.assertFalse(answered_well("unanswerable", self.LONG))

    def test_partial_needs_both_gap_and_content(self) -> None:
        partial = "作業場のルールは次の通りです。" + "あ" * 150 + "\nなお費用の記載がありません。"
        self.assertTrue(answered_well("partial", partial))
        # 欠落を認めるだけで中身が無いものは不合格
        self.assertFalse(answered_well("partial", self.MISSING_ONLY))

    def test_false_premise_accepts_correction_without_missing_phrase(self) -> None:
        self.assertTrue(answered_well("false_premise", "滑空機ではなく人力プロペラ機です。"))

    def test_normal_type_fails_only_on_bare_refusal(self) -> None:
        self.assertTrue(answered_well("fact", self.LONG))
        self.assertFalse(answered_well("fact", self.MISSING_ONLY))

    def test_citation_lines_do_not_pad_the_length(self) -> None:
        """出典行で字数を稼いで「実質的に答えている」と誤判定させない。"""
        text = self.MISSING_ONLY + "\n\n" + CITATION_NOW * 3
        self.assertFalse(answered_well("fact", text))


class BodyOnlyTest(unittest.TestCase):
    def test_drops_both_citation_forms(self) -> None:
        self.assertEqual(body_only(f"本文。\n\n{CITATION_NOW}"), "本文。")
        self.assertEqual(body_only(f"本文。\n\n{CITATION_OLD}\n続きも出典欄"), "本文。")


if __name__ == "__main__":
    unittest.main()


class RetrievalScoresTest(unittest.TestCase):
    """節単位の採点。**「1件でも拾えた」と「全部拾えた」を混同しない。**

    ページ単位のRecallが実力を17ポイント過大評価したのと同じ取り違えが、
    チャンク単位でも起きていた（chunk_recall の実体は Hit@k だった）。
    """

    def test_gold無しは採点対象外(self):
        self.assertIsNone(retrieval_scores([], ["p1-c1"]))

    def test_一部だけ拾えた場合(self):
        s = retrieval_scores(["a", "b"], ["a", "x", "y"])
        self.assertTrue(s["hit"])
        self.assertFalse(s["all_evidence"])
        self.assertEqual(s["recall"], 0.5)
        self.assertAlmostEqual(s["precision"], 1 / 3)

    def test_全部拾えた場合(self):
        s = retrieval_scores(["a", "b"], ["b", "a"])
        self.assertTrue(s["all_evidence"])
        self.assertEqual(s["recall"], 1.0)
        self.assertEqual(s["precision"], 1.0)

    def test_1件も拾えない場合(self):
        s = retrieval_scores(["a"], ["x"])
        self.assertFalse(s["hit"])
        self.assertEqual(s["recall"], 0.0)
        self.assertEqual(s["precision"], 0.0)

    def test_節を1件も渡していない場合は0で割らない(self):
        s = retrieval_scores(["a"], [])
        self.assertEqual(s["precision"], 0.0)


class JudgeCalibrationTest(unittest.TestCase):
    """**false-accept を最重視する。** 引き継ぎ資料では、答えられないことより
    間違って答えることのほうがはるかに有害である（docs/01 §5-3）。"""

    def test_全員同じ判定ならκは算出できない(self):
        self.assertIsNone(cohens_kappa([(True, True), (True, True)]))

    def test_完全一致のκは1(self):
        self.assertEqual(cohens_kappa([(True, True), (False, False)]), 1.0)

    def test_偶然の一致を差し引く(self):
        # 8割一致でも、偏りが強ければκは1にならない
        pairs = [(True, True)] * 8 + [(False, True), (True, False)]
        kappa = cohens_kappa(pairs)
        self.assertIsNotNone(kappa)
        self.assertLess(kappa, 0.5)

    def test_人手が裏付けなしと見たものを通したらfalse_accept(self):
        counts = confusion([(False, True), (True, True), (False, False), (True, False)])
        self.assertEqual(counts["false_accept"], 1)
        self.assertEqual(counts["false_reject"], 1)
        self.assertEqual(counts["true_accept"], 1)
        self.assertEqual(counts["true_reject"], 1)


class CitationScoresTest(unittest.TestCase):
    """出典の測り方を本番へ合わせる。

    **本番は回答内に出典一覧を作らせない。** 題名とURLはサーバーが索引から
    組み立ててカードで出すので、モデルが書けるのは番号だけである。以前ここは
    「末尾のMarkdown出典行があるか」を見ており、本番が禁じている振る舞いを
    合格にしていた。
    """

    def test_実在する番号だけを数える(self):
        scores = citation_scores("翼型はDAE31です[1]。桁は[2][3]です。", 2)
        self.assertEqual(scores["in_range"], 2)
        self.assertEqual(scores["out_of_range"], 1)

    def test_出典一覧は規則違反として数える(self):
        scores = citation_scores("- [ページ](https://example.com)（Wiki）", 2)
        self.assertEqual(scores["source_list"], 1)
        self.assertEqual(scores["in_range"], 0)

    def test_一般知識の節にある番号は数えない(self):
        # 一般知識は資料に紐づかない。ここの番号まで数えると、
        # 資料に基づく引用の割合が実態より高く出る
        text = "翼型はDAE31です[1]。\n\n## 一般知識（WASA資料外）\n\n誘導抗力は[2]で説明される。"
        self.assertEqual(citation_scores(text, 2)["in_range"], 1)

    def test_番号が無ければ0(self):
        self.assertEqual(citation_scores("資料に記載がありません。", 3)["in_range"], 0)


class GoldenCheckTest(unittest.TestCase):
    """ゴールデンデータと索引の整合。

    **チャンクIDは `p{ページ}-c{連番}` で、索引を作り直すとずれ得る。**
    ずれたまま測ると、検索が当たっているのに外れと数える。
    """

    INDEX = {"pages": [
        {"title": "空力設計", "aliases": ["空力"], "chunks": [{"id": "p1-c1"}, {"id": "p1-c2"}]},
        {"title": "翼班", "aliases": [], "chunks": [{"id": "p2-c1"}]},
    ]}

    def check(self, question):
        return check_golden([question], self.INDEX)

    def test_整合していれば何も出ない(self):
        self.assertEqual(self.check({
            "id": "q1", "type": "factual", "answerable": True,
            "evidence_pages": ["空力設計"], "evidence_chunks": ["p1-c1"],
        }), [])

    def test_索引に無いチャンクを指摘する(self):
        problems = self.check({
            "id": "q1", "type": "factual", "answerable": True,
            "evidence_pages": ["空力設計"], "evidence_chunks": ["p9-c9"],
        })
        self.assertEqual(len(problems), 1)
        self.assertIn("p9-c9", problems[0])

    def test_チャンクと正解ページの食い違いを指摘する(self):
        # IDがずれたとき、ここが真っ先に食い違う
        problems = self.check({
            "id": "q1", "type": "factual", "answerable": True,
            "evidence_pages": ["空力設計"], "evidence_chunks": ["p2-c1"],
        })
        self.assertEqual(len(problems), 1)
        self.assertIn("翼班", problems[0])

    def test_別名で書かれていたら別名だと教える(self):
        problems = self.check({
            "id": "q1", "type": "factual", "answerable": True,
            "evidence_pages": ["空力"], "evidence_chunks": ["p1-c1"],
        })
        self.assertIn("別名として存在", problems[0])

    def test_全域型は正解チャンクが空でよい(self):
        # 「最も情報が薄い分野は」の根拠は目次そのもので、特定の節ではない
        self.assertEqual(self.check({
            "id": "q31", "type": "global", "answerable": True,
            "evidence_pages": [], "evidence_chunks": [],
        }), [])

    def test_全域型以外で正解チャンクが空なら指摘する(self):
        problems = self.check({
            "id": "q1", "type": "factual", "answerable": True,
            "evidence_pages": [], "evidence_chunks": [],
        })
        self.assertEqual(len(problems), 1)
