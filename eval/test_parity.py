"""本番Go（backend/internal/pipeline）と測定用Python（rag/pipeline.py）のずれを検出する。

  python -m unittest eval.test_parity

**送信は0回。** ソースを読んで突き合わせるだけ。

なぜ要るのか
------------
この2つは「検索・回答の段構成とコアプロンプトを揃える」と決めてある。揃っていないと、
**Pythonで測った数字が本番を説明しない**。片方だけ直す事故は繰り返し起きている。

  M24  決定的候補の合流順を変えると 19/33 → 18/33 に退行すると実測した
  M52  それなのに Python 側は「型番 → 実在タイトル」のままで、リンク検索も無かった
  M55  出所の絞り込み（questionAllowsOrigin）が Python 側に存在しなかった

どちらも**読めば分かるのに、読まないと分からない**種類の食い違いである。
ここで機械的に突き合わせておけば、片方を直した瞬間に落ちる。

⚠️ **「揃っていること」を強制する対象は、意図して揃えると決めたものだけ。**
本番固有のもの（system規則、参照範囲、SSE）はGoにしか無くてよい。
"""

from __future__ import annotations

import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
GO = (ROOT / "backend" / "internal" / "pipeline" / "pipeline.go").read_text(encoding="utf-8")
PY = (ROOT / "rag" / "pipeline.py").read_text(encoding="utf-8")


def go_number(name: str) -> str:
    match = re.search(name + r"\s*=\s*([0-9][0-9_]*)", GO)
    return match.group(1).replace("_", "") if match else ""


def py_number(name: str) -> str:
    match = re.search(name + r"\s*=\s*([0-9][0-9_]*)", PY)
    return match.group(1).replace("_", "") if match else ""


def go_regexp(name: str) -> str:
    match = re.search(name + r"\s*=\s*regexp\.MustCompile\(`([^`]*)`\)", GO)
    # Goの (?i) はPythonの re.IGNORECASE に対応する。比較では外す
    return match.group(1).replace("(?i)", "") if match else ""


def py_regexp(name: str) -> str:
    match = re.search(name + r'\s*=\s*re\.compile\(\s*\n?\s*r"([^"]*)"', PY)
    return match.group(1) if match else ""


def section(text: str, heading: str, until: str) -> str:
    """プロンプトの1節を取り出す。見出しから次の見出しまで。

    Python側は f-string で `{MAX_PAGES}` のように書くので、**展開してから比べる。**
    ここを揃えないと、同じ内容でも書き方の違いで落ちる。
    """
    start = text.index(heading)
    end = text.index(until, start)
    body = text[start:end].strip()
    # Goは raw string（バッククォート）、Pythonは f-string の終端が混ざる。落とす
    body = body.rstrip("`\"' \n\t")
    for name, value in (("MAX_PAGES", py_number("MAX_PAGES")),
                        ("MAX_CHUNKS", py_number("MAX_CHUNKS"))):
        body = body.replace("{" + name + "}", value)
    return "\n".join(line.rstrip() for line in body.split("\n")).strip()


class NumbersTest(unittest.TestCase):
    """検索の効き方を決める数値。片方だけ変えると、測定値が本番を説明しなくなる。"""

    CASES = [
        ("最大ページ数", "maxPages", "MAX_PAGES"),
        ("節をそのまま渡す上限（字）", "directContextLimit", "DIRECT_CONTEXT_LIMIT"),
        ("最大チャンク数", "maxChunks", "MAX_CHUNKS"),
        # 実在タイトル候補の優先順位。値ではなく大小関係が意味を持つ
        ("世代と分野が合う", "scoreGenerationAndField", "SCORE_GENERATION_AND_FIELD"),
        ("分野が合い世代つき", "scoreTitleWithGeneration", "SCORE_TITLE_WITH_GENERATION"),
        ("分野だけ合う", "scoreTitleOnly", "SCORE_TITLE_ONLY"),
        ("その他", "scoreWeakMatch", "SCORE_WEAK_MATCH"),
    ]

    def test_数値が一致する(self):
        for label, go_name, py_name in self.CASES:
            with self.subTest(label):
                go_value, py_value = go_number(go_name), py_number(py_name)
                self.assertTrue(go_value, f"Go側に {go_name} が見つからない")
                self.assertTrue(py_value, f"Python側に {py_name} が見つからない")
                self.assertEqual(go_value, py_value, f"{label} がずれている")


class RegexpTest(unittest.TestCase):
    """質問の字面から候補を決める正規表現。ここがずれると、選ぶページが変わる。"""

    CASES = [
        ("型番の抽出", "identifierPattern", "IDENTIFIER_PATTERN"),
        ("40th → 40代", "generationOrdinalPattern", "GENERATION_ORDINAL_PATTERN"),
        ("代のラベル", "generationLabelPattern", "GENERATION_LABEL_PATTERN"),
        ("リンクを尋ねる質問", "linkRequestPattern", "LINK_REQUEST_PATTERN"),
        ("深く考える質問", "deepQuestionPattern", "DEEP_QUESTION_PATTERN"),
    ]

    def test_正規表現が一致する(self):
        for label, go_name, py_name in self.CASES:
            with self.subTest(label):
                go_value, py_value = go_regexp(go_name), py_regexp(py_name)
                self.assertTrue(go_value, f"Go側に {go_name} が見つからない")
                self.assertTrue(py_value, f"Python側に {py_name} が見つからない")
                self.assertEqual(go_value, py_value, f"{label} の正規表現がずれている")


class PromptTest(unittest.TestCase):
    """コアプロンプト。**ここがずれていたことに、目視で気づくのは難しい。**"""

    def test_ページ選択のプロンプトが一致する(self):
        go = section(GO, "# 役割", "var selectSchema")
        py = section(PY, "# 役割", '"""')
        self.assertEqual(go, py, "ページ選択のプロンプトがずれている")

    def test_回答の出力規則が一致する(self):
        # 出典の書き方をPythonだけ変えていた事故がある（M52）。
        # 本番は「回答内に出典一覧を作らない」と禁じているのに、Python側は要求していた
        go = section(GO, "# 出力の規則", "# 資料")
        py = section(PY, "# 出力の規則", "# 資料")
        self.assertEqual(go, py, "回答の出力規則がずれている")

    def test_根拠の規則と年代の規則が一致する(self):
        for heading, until in (("# 根拠の規則", "# 年代の規則"), ("# 年代の規則", "# 出力の規則")):
            with self.subTest(heading):
                go = section(GO, heading, until)
                py = section(PY, heading, until)
                # 一般知識の扱いだけは、本番がsystem側へ出しているので文言が違ってよい
                go = re.sub(r"^- WASA固有の事実を資料外で補わない。.*$", "", go, flags=re.M)
                py = re.sub(r"^- WASA固有の事実を資料外で補わない。.*$", "", py, flags=re.M)
                self.assertEqual(go.strip(), py.strip(), f"{heading} がずれている")


class BehaviourTest(unittest.TestCase):
    """規則そのものではなく、**規則を適用する場所**が揃っているか。

    M52・M55で見つかったのは、規則の文言ではなく「どこで効かせるか」の食い違いだった。
    """

    def test_決定的候補の合流順が一致する(self):
        # M24: 順序を変えると 19/33 → 18/33 に退行する
        self.assertIn("directTitlePages(ix, question, a), identifierPages(ix, question, a), linkPages(ix, question, a)", GO)
        self.assertIn("self.direct_title_pages(question)", PY)
        order = re.search(
            r"for title in \(\s*self\.direct_title_pages\(question\)\s*\+ self\.identifier_pages\(question\)\s*\+ self\.link_pages\(question\)\s*\)",
            PY,
        )
        self.assertTrue(order, "Python側の合流順が「実在タイトル → 型番 → リンク」になっていない")

    def test_出所の絞り込みが両方にある(self):
        self.assertIn("func questionAllowsOrigin(", GO)
        self.assertIn("def question_allows_origin(", PY)

    def test_救済経路でも出所を絞る(self):
        # ほかの候補は add() を通るが、fallback はそのまま使われるため抜けやすい
        fallback_go = GO[GO.index("func fallbackPages("):]
        self.assertIn("questionAllowsOrigin(question, pg)", fallback_go[:2000])
        fallback_py = PY[PY.index("def fallback_pages("):]
        self.assertIn("self.question_allows_origin(question, title)", fallback_py[:2000])


if __name__ == "__main__":
    unittest.main()
