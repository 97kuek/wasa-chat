"""検索の評価ハーネス。ゴールデンデータに対して Evidence Recall などを算出する。

設計方針 docs/01-設計方針.md §5-3 に対応。

**採点と検索は分離してある。** 採点器（score）は「質問 → チャンクIDの順位リスト」を
返す任意の関数を受け取る。したがって、後段でGoに実装するBM25の結果を
JSONで書き出せば、同じ採点器でそのまま比較できる。Python側の実装は
あくまで測定の足場であって、本番の検索器ではない。

同梱のベースライン検索器は **文字bigramのBM25**。形態素解析器を使わないのは、
本番のGo側は kagome を使う予定であり、ここでSudachiPyを持ち込むと
「索引と検索で分かち書きが食い違う」問題を評価にも持ち込むことになるため。
日本語では文字bigramでも語彙検索の下限として十分機能する。

  python eval/retrieval_eval.py
"""

from __future__ import annotations

import json
import math
import re
import sys
from collections import Counter, defaultdict
from pathlib import Path
from typing import Callable

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from rag.pipeline import Pipeline

INDEX = Path("data/index.json")
TOC = Path("data/toc.md")
GOLDEN = Path("eval/golden.json")
K_VALUES = (1, 3, 5, 10)

# 検索器の型: 質問文 → チャンクIDを関連度順に並べたリスト
Retriever = Callable[[str], list[str]]


def question_with_history(question: dict) -> str:
    """多ターン問題は、直近の会話と現在の質問を本番と同じ順で検索へ渡す。"""
    history = question.get("history") or []
    if not history:
        return question["question"]
    lines = ["# 直近の会話（参照解決用）"]
    for turn in history:
        lines += [f"利用者: {turn['question']}", f"以前の回答: {turn['answer']}"]
    lines += ["# 現在の質問", question["question"]]
    return "\n".join(lines)


def tokenize(text: str) -> list[str]:
    """日本語は文字bigram、英数字は単語単位で切る。

    形態素解析器を使わない代わりに、CJKは文字bigramで近似する。
    「主翼桁」が「主翼」「翼桁」に分かれるため、部分一致も拾える。
    """
    text = text.lower()
    tokens: list[str] = []
    for run in re.findall(r"[a-z0-9]+|[^\sa-z0-9]+", text):
        if re.match(r"^[a-z0-9]+$", run):
            tokens.append(run)
        else:
            cjk = re.sub(r"[\s　、。，．・「」（）()｜|*#>\[\]:：/]", "", run)
            tokens += [cjk[i : i + 2] for i in range(len(cjk) - 1)] or ([cjk] if cjk else [])
    return tokens


class BM25:
    """文字bigramに対する BM25。117ページ規模なら総当たりで一瞬。"""

    def __init__(self, docs: dict[str, str], k1: float = 1.2, b: float = 0.75):
        self.k1, self.b = k1, b
        self.ids = list(docs)
        self.tf = {i: Counter(tokenize(t)) for i, t in docs.items()}
        self.len = {i: sum(c.values()) for i, c in self.tf.items()}
        self.avgdl = sum(self.len.values()) / len(self.len)

        df: Counter[str] = Counter()
        for counts in self.tf.values():
            df.update(counts.keys())
        n = len(self.ids)
        self.idf = {t: math.log(1 + (n - d + 0.5) / (d + 0.5)) for t, d in df.items()}

    def search(self, query: str) -> list[str]:
        scores: dict[str, float] = defaultdict(float)
        for token in tokenize(query):
            idf = self.idf.get(token)
            if idf is None:
                continue
            for doc_id, counts in self.tf.items():
                freq = counts.get(token)
                if not freq:
                    continue
                norm = 1 - self.b + self.b * self.len[doc_id] / self.avgdl
                scores[doc_id] += idf * freq * (self.k1 + 1) / (freq + self.k1 * norm)
        return [i for i, _ in sorted(scores.items(), key=lambda kv: -kv[1])]


def score(questions: list[dict], retrieve: Retriever, chunk_page: dict[str, str]) -> dict:
    """Evidence Hit / Evidence Recall / All-Evidence / Page Recall / MRR を算出する。

    ページ単位のRecallも併記する。36,261字ある「駆動・フレーム班」が
    ヒットしただけで正解扱いになる指標では実力が測れないことを、
    数字の差として見えるようにするため。

    ⚠️ **Hit と Recall を混同しないこと。**
    - Evidence **Hit**@k    : 正解の節を1件でも拾えた質問の割合
    - Evidence **Recall**@k : 正解の節のうち何割を拾えたか（根拠の総数で割る）

    以前は前者を `chunk_recall` という名前で持ち、Recall として報告していた。
    ページ単位のRecallが実力を17ポイント過大評価したのと同じ取り違えである（M1）。
    docs/08 の「BM25 Evidence Recall@5 51.5%」は**Hit@5の値**である。
    """
    scored = [q for q in questions if q["evidence_chunks"]]
    out = {
        "n": len(scored),
        "skipped": [q["id"] for q in questions if not q["evidence_chunks"]],
        "evidence_hit": {}, "evidence_recall": {}, "all_evidence_recall": {}, "page_recall": {},
        "gold_total": {}, "found_total": {},
        "mrr": 0.0, "per_type": defaultdict(lambda: {"n": 0, "hit@5": 0}), "misses": [],
    }

    reciprocal = 0.0
    for q in scored:
        ranked = retrieve(question_with_history(q))
        gold = set(q["evidence_chunks"])
        gold_pages = {chunk_page[c] for c in gold if c in chunk_page}

        for k in K_VALUES:
            top = ranked[:k]
            out["evidence_hit"].setdefault(k, 0)
            out["all_evidence_recall"].setdefault(k, 0)
            out["page_recall"].setdefault(k, 0)
            out["gold_total"].setdefault(k, 0)
            out["found_total"].setdefault(k, 0)
            out["gold_total"][k] += len(gold)
            out["found_total"][k] += len(gold & set(top))
            if gold & set(top):
                out["evidence_hit"][k] += 1
            if gold <= set(top):
                out["all_evidence_recall"][k] += 1
            if gold_pages & {chunk_page[c] for c in top if c in chunk_page}:
                out["page_recall"][k] += 1

        rank = next((i + 1 for i, c in enumerate(ranked) if c in gold), None)
        reciprocal += 1 / rank if rank else 0
        bucket = out["per_type"][q["type"]]
        bucket["n"] += 1
        bucket["hit@5"] += 1 if gold & set(ranked[:5]) else 0
        if not rank or rank > 5:
            out["misses"].append((q["id"], q["question"][:34], rank))

    out["mrr"] = reciprocal / len(scored) if scored else 0.0
    out["evidence_recall"] = {
        k: (out["found_total"][k] / out["gold_total"][k] if out["gold_total"][k] else 0.0)
        for k in K_VALUES
    }
    return out


def report(name: str, result: dict) -> None:
    n = result["n"]
    pct = lambda x: f"{x / n * 100:5.1f}%"
    print(f"\n{'=' * 62}\n{name}  (採点対象 {n}問)\n{'=' * 62}")
    print(f"{'k':>3} | {'Evidence Hit':>13} | {'Evidence Recall':>16} | "
          f"{'All-Evidence':>13} | {'Page Recall':>12}")
    print("-" * 78)
    for k in K_VALUES:
        print(f"{k:>3} | {pct(result['evidence_hit'][k]):>13} | "
              f"{result['evidence_recall'][k] * 100:15.1f}% | "
              f"{pct(result['all_evidence_recall'][k]):>13} | {pct(result['page_recall'][k]):>12}")
    print("  Hit＝1件でも拾えた質問の割合 / Recall＝拾えた根拠の割合。混同しないこと")
    print(f"\nMRR: {result['mrr']:.3f}")

    print("\n種別ごとの Hit@5:")
    for qtype, v in sorted(result["per_type"].items()):
        bar = "#" * round(v["hit@5"] / v["n"] * 20)
        print(f"  {qtype:<14} {v['hit@5']:>2}/{v['n']:<2} {bar}")

    if result["misses"]:
        print(f"\n上位5件で拾えなかった質問 ({len(result['misses'])}件):")
        for qid, text, rank in result["misses"]:
            print(f"  {qid}  {text:<36} 正解の順位: {rank or '圏外'}")
    if result["skipped"]:
        print(f"\n検索評価の対象外（根拠チャンクを持たない設問）: {', '.join(result['skipped'])}")


def report_deterministic_pages(questions: list[dict], pipeline: Pipeline) -> None:
    """LLM選択の成否に関係なく、正解ページを保持できる設問数を出す。

    最終的なPage Recallではない。この候補にLLMが選んだ2件以上を合流するため、
    「モデルが誤選択しても落ちない下限」として分けて記録する。
    """
    scored = [question for question in questions if question["evidence_pages"]]
    print(f"\n{'=' * 62}\n決定的ページ候補  (採点対象 {len(scored)}問)\n{'=' * 62}")
    for name, retrieve in (
        ("型番の本文一致のみ", pipeline.identifier_pages),
        ("型番 + 質問中の実在タイトル", pipeline.deterministic_pages),
    ):
        hits = 0
        candidates = 0
        misses: list[str] = []
        for question in scored:
            pages = retrieve(question_with_history(question))
            candidates += len(pages)
            if set(pages) & set(question["evidence_pages"]):
                hits += 1
            else:
                misses.append(question["id"])
        print(
            f"  {name:<28} {hits:>2}/{len(scored)} = {hits / len(scored) * 100:>4.1f}%"
            f" / 平均{candidates / len(scored):.2f}件"
        )
        print(f"    未保証: {', '.join(misses)}")
    print("  候補上限: 2件（LLM選択用に最低2枠を残す）")


def main() -> None:
    index = json.loads(INDEX.read_text(encoding="utf-8"))
    questions = json.loads(GOLDEN.read_text(encoding="utf-8"))["questions"]

    chunks = {c["id"]: c["text"] for p in index["pages"] for c in p["chunks"]}
    chunk_page = {c["id"]: p["title"] for p in index["pages"] for c in p["chunks"]}
    print(f"チャンク {len(chunks)} 件 / 設問 {len(questions)} 問を読み込み")

    bm25 = BM25(chunks)
    report("ベースライン: BM25（文字bigram）", score(questions, bm25.search, chunk_page))

    report_deterministic_pages(questions, Pipeline(INDEX, TOC, None))

    # 下限の目安。これを明確に上回らない検索器は、検索していないのと同じ
    order = list(chunks)
    report("参考: 無情報（インデックス順）", score(questions, lambda q: order, chunk_page))


if __name__ == "__main__":
    main()
