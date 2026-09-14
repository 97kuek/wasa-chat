"""M2b: エンドツーエンドの回答品質を測る。

設計方針 docs/01-設計方針.md §5-3 に対応。評価は2層に分ける。

  1. ルールベース（決定的・無料）
     - 出典を明示したか
     - 回答不能な問いで「記載がない」と言えたか（ハルシネーション検出）
     - 検索段でのページ選択が当たっているか

  2. LLM-as-a-Judge（Faithfulness）
     - 回答が渡した資料だけで裏付けられるか
     - ⚠️ **判定器がローカルモデルの間は暫定値**。設計方針 §5-3 の通り、
       人手評価との一致率（κ）を測るまで、この数字は指標として信用しない

  ollama serve  # OLLAMA_CONTEXT_LENGTH=32768 で起動しておく
  python eval/answer_eval.py
出力: eval/answers.json（人手レビュー用。Wiki由来の内容を含むため .gitignore 対象）
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from collections import defaultdict
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from dotenv import load_dotenv  # noqa: E402
from rag.llm import make_llm  # noqa: E402
from rag.pipeline import Pipeline  # noqa: E402

GOLDEN = Path("eval/golden.json")
OUT = Path("eval/answers.json")

# 「記載がない」と言えているかの検出。表現の揺れを拾う
NO_INFO = re.compile(
    r"記載(が|は)?(あり|ござい)?ませ|記述(が|は)?(あり|ござい)?ませ|書かれてい(ませ|ない)|"
    r"見当たりませ|情報(が|は)(あり|ござい)?ませ|特定できませ|含まれてい(ませ|ない)"
)
# 出典行の検出。M7で出力形式を
# `- [ページ名](URL)（Wiki / 公式サイト、本文の年代: YYYY年）` へ変えたのに、
# ここが旧形式の「出典:」のままだったため、正しく出典を出している回答を
# 0%と報告していた（2026-08-09に発覚）。新旧どちらの形式も拾う。
CITATION_LINE = re.compile(
    r"^\s*[-*]\s*\[[^\]]+\]\(https?://[^)]+\).*$"   # 現行: Markdownリンクの出典行
    r"|^\s*出典[:：].*$",                            # 旧形式
    re.M,
)
CITATION = CITATION_LINE


# 「一般知識（WASA資料外）」の節。2026-08-09にアシスタントでも一般知識を
# 許したため、ここを判定対象へ残すと**正しい一般知識が必ず「裏付けなし」になる**。
# Faithfulnessは資料に対する忠実性であって、一般論の正しさは測れない
# （docs/01 §9。一般部分は少量の人手評価で見る）。
GENERAL_SECTION = re.compile(r"^\s*#{0,6}\s*\**\s*一般知識（WASA資料外）.*$", re.M)


def strip_general_knowledge(text: str) -> str:
    """一般知識の見出し以降を落とす。見出しが無ければそのまま返す。"""
    m = GENERAL_SECTION.search(text)
    return text[: m.start()].strip() if m else text


# 本文に差し込まれた資料番号。`[1]` や `[1][3]` の形で文末に付く。
CITATION_MARK = re.compile(r"\s*(?:\[\d{1,2}\])+")


def strip_citations(text: str) -> str:
    """出典行と資料番号を落とす。どちらもメタデータであって、回答の主張ではない。

    ⚠️ **測定器の欠陥（5回目）。** 2026-08-11に本文中の資料番号（`[1]`）を
    足したとき、ここを直し忘れた。Judgeへ番号が付いたまま渡っており、
    「初めて出られなかった代は1993年（9代）です [1]」のような**正しい文を
    そのまま unsupported_claim へ引用**してくる。Faithfulnessが
    32/33（M23）から31/35へ落ちて見えたのは、これが主因だった。

    出典行を外す理由（M3）とまったく同じ理屈である。番号は「どの資料に
    基づくか」を示すメタデータで、資料と突き合わせる対象の主張ではない。
    """
    return CITATION_MARK.sub("", CITATION_LINE.sub("", text)).strip()


def body_only(text: str) -> str:
    """出典欄を除いた本文。出典に含まれる語で判定が揺れるのを防ぐ。"""
    return strip_citations(re.sub(r"出典[:：].*", "", text, flags=re.S))


def answered_well(qtype: str, text: str) -> bool:
    """回答の出し方が種別に対して適切かを判定する。

    ⚠️ 当初は「本文に『記載がありません』が出たら拒否」と一律に数えていたが、
    プロンプトは「何が書かれていて何が書かれていないかを分けて述べる」と
    指示している。つまり**指示どおりに答えた回答ほど誤検知される**構造だった。
    種別ごとに基準を分ける。
    """
    body = body_only(text)
    says_missing = bool(NO_INFO.search(body))
    if qtype == "unanswerable":
        return says_missing and len(body) < 300          # 明確に断る
    if qtype == "partial":
        return says_missing and len(body) >= 120         # 欠落を認めつつ中身も出す
    if qtype == "false_premise":
        return says_missing or "ではなく" in body        # 前提の誤りを指摘する
    return not (says_missing and len(body) < 160)        # 実質的に答えている

def citation_scores(text: str, source_count: int) -> dict:
    """本文中の資料番号（`[n]`）を数える。

    ⚠️ **本番は回答内に出典一覧を作らせない。** 出典の題名とURLはサーバーが索引から
    組み立ててカードで出すので、モデルが書けるのは番号だけである（docs/02）。
    以前ここは「末尾のMarkdown出典行があるか」を見ていたが、それは測定用Python
    だけが要求していた形式で、**本番が禁じている振る舞いを合格にしていた**
    （2026-09-12に修正。当時の作業リストが指摘していたずれ）。

    - in_range     : 実在する資料番号の数
    - out_of_range : 存在しない番号の数（**0であるべき**。M39で222個中0個）
    - source_list  : 出典一覧を作ってしまった回数（本番では規則違反）
    """
    numbers = [int(n) for n in re.findall(r"\[(\d{1,2})\]", strip_general_knowledge(text))]
    return {
        "in_range": sum(1 for n in numbers if 1 <= n <= source_count),
        "out_of_range": sum(1 for n in numbers if not (1 <= n <= source_count)),
        "source_list": len(CITATION_LINE.findall(text)),
    }


def retrieval_scores(gold_chunks: list[str], picked_chunks: list[str]) -> dict | None:
    """検索段を**節単位**で採点する。gold が無い設問は None（採点対象外）。

    ⚠️ **「1件でも拾えたか」と「必要な根拠を全部拾えたか」を混同しないこと。**
    これまで Evidence Recall と呼んでいた値は、実体が質問単位の Hit@k だった
    （eval/retrieval_eval.py の chunk_recall）。ページ単位のRecallが実力を
    17ポイント過大評価したのと同じ性質の取り違えである（M1）。

    - hit           : 正解の節を1件でも拾えたか
    - recall        : 拾えた正解の節 ÷ 正解の節（この設問の割合）
    - all_evidence  : 正解の節をすべて拾えたか
    - precision     : 渡した節のうち正解だった割合（Context Precision）
    """
    gold = set(gold_chunks)
    if not gold:
        return None
    picked = set(picked_chunks)
    found = gold & picked
    return {
        "hit": bool(found),
        "found": len(found),
        "gold": len(gold),
        "recall": len(found) / len(gold),
        "all_evidence": gold <= picked,
        "picked": len(picked),
        "precision": (len(found) / len(picked)) if picked else 0.0,
    }


FAITHFUL_SCHEMA = {
    "type": "object",
    "properties": {
        # 理由を先に書かせる。判定を先に置くと、根拠を考える前に決めてしまい
        # 判定基準の文をそのままオウム返しする（M2bで実際に起きた）
        "unsupported_claim": {"type": "string"},
        "faithful": {"type": "boolean"},
    },
    "required": ["unsupported_claim", "faithful"],
}


def judge_faithfulness(llm, context: str, answer: str, toc: str = "") -> dict:
    # 出典行はメタデータであって主張ではない。判定対象から外さないと
    # 「出典: X（最終更新: Y）」そのものを裏付け無しと指摘してくる（M3で対処済み）。
    # M7で出典形式をMarkdownリンクへ変えた際、ここも新形式に合わせる必要がある
    answer = strip_general_knowledge(strip_citations(answer))
    prompt = f"""以下の「資料」と「回答」を読み、回答の内容が資料だけで裏付けられるかを判定してください。

手順:
1. 回答の中に、資料で裏付けられない具体的な記述があれば、その一文を
   unsupported_claim にそのまま引用する。無ければ空文字列にする
2. unsupported_claim が空なら faithful = true、そうでなければ false

注意:
- 「記載がない」と述べているだけなら faithful = true
- 資料の要約・言い換えは faithful = true

# 資料
{toc}

{context}

# 回答
{answer}
"""
    result = llm(prompt, FAITHFUL_SCHEMA, 300)
    claim = (result.get("unsupported_claim") or "").strip()
    # **判定は「裏付けの無い一文を引用できたか」で決める。** モデルの faithful を
    # そのまま使わないのは、理由を書かずに false と言う出力があるためである。
    # ただし食い違い自体は数える。判定器を校正するとき、どちらが外しているかの
    # 手がかりになる（docs/01 §5-3「判定器を無条件に信じない」）
    claimed = bool(result.get("faithful"))
    return {"faithful": not claim, "reason": claim, "judge_disagreed": claimed == bool(claim)}


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="回答品質をゴールデンデータで測定する")
    parser.add_argument(
        "--ids",
        help="測定する設問IDをカンマ区切りで指定する（例: q32,q33）。省略時は全問",
    )
    parser.add_argument(
        "--repeats",
        type=int,
        default=1,
        help="同じ設問を何回測るか（既定: 1）。2以上にすると各回の値と範囲を出す",
    )
    parser.add_argument(
        "--oracle",
        action="store_true",
        help="検索を通さず、ゴールデンの正解チャンクだけで回答させる（生成だけを測る）",
    )
    parser.add_argument(
        "--output",
        type=Path,
        default=OUT,
        help=f"回答詳細の出力先（既定: {OUT}）",
    )
    return parser.parse_args()


def measure(pipeline, llm, questions: list[dict], oracle: bool = False) -> tuple[list[dict], dict, dict]:
    """1回分の測定。records / stats / 種別ごとの内訳を返す。

    oracle=True のときは検索を通さず、ゴールデンの正解チャンクを直接渡して
    **生成だけ**を測る。検索段の失敗と生成段の失敗を切り分けるため（docs/01 §5-1）。
    """
    records = []
    stats = {
        "page_hit": 0, "page_scored": 0,
        "cited": 0, "answered_well": 0, "faithful": 0,
        "marks_in_range": 0, "out_of_range": 0, "source_list": 0,
        # 節単位。分母は gold のある設問だけなので別に数える
        "chunk_scored": 0, "evidence_hit": 0, "all_evidence": 0,
        "gold_total": 0, "found_total": 0, "picked_total": 0,
        "judge_disagreed": 0,
        "dropped": [],
    }
    per_type = defaultdict(lambda: {"n": 0, "page_hit": 0, "faithful": 0})

    for q in questions:
        if oracle:
            answer = pipeline.answer_with_chunks(q["question"], list(q["evidence_chunks"]), q.get("history"))
        else:
            answer = pipeline.answer(q["question"], q.get("history"))
        gold = set(q["evidence_pages"])
        picked = set(answer.pages)

        # --- ルールベース ---
        page_hit = bool(gold & picked) if gold else None
        if page_hit is not None:
            stats["page_scored"] += 1
            stats["page_hit"] += page_hit
        marks = citation_scores(answer.text, len(answer.pages))
        cited = marks["in_range"] > 0
        # **キー名と中身を一致させる。** 以前は `said_no_info` という名前に
        # 「適切に答えられたか」を入れ、保存JSONには逆の意味の値を入れていた。
        # 同じキーが集計と保存で逆を指す状態だった（2026-09-12に発見。測定器の欠陥6件目）
        well = answered_well(q["type"], answer.text)
        stats["cited"] += cited
        stats["answered_well"] += well
        stats["marks_in_range"] += marks["in_range"]
        stats["out_of_range"] += marks["out_of_range"]
        stats["source_list"] += marks["source_list"]
        stats["dropped"] += [(q["id"], t) for t in answer.dropped_titles]

        # --- 検索段を節単位で採点する（oracleでは意味がないので飛ばす） ---
        scores = None if oracle else retrieval_scores(q["evidence_chunks"], answer.chunk_ids)
        if scores:
            stats["chunk_scored"] += 1
            stats["evidence_hit"] += scores["hit"]
            stats["all_evidence"] += scores["all_evidence"]
            stats["gold_total"] += scores["gold"]
            stats["found_total"] += scores["found"]
            stats["picked_total"] += scores["picked"]

        # --- LLM-as-a-Judge（暫定） ---
        context = "\n\n".join(pipeline.chunks[c]["text"] for c in answer.chunk_ids)
        verdict = judge_faithfulness(llm, context[:20000], answer.text, pipeline.toc) \
            if answer.chunk_ids else {"faithful": True, "reason": "資料なし"}
        stats["faithful"] += verdict["faithful"]
        stats["judge_disagreed"] += verdict.get("judge_disagreed", False)

        bucket = per_type[q["type"]]
        bucket["n"] += 1
        bucket["page_hit"] += bool(page_hit)
        bucket["faithful"] += verdict["faithful"]

        records.append({
            "id": q["id"], "type": q["type"], "question": q["question"],
            "expected": q["expected"], "answer": answer.text,
            "pages": answer.pages, "gold_pages": q["evidence_pages"],
            "chunk_ids": answer.chunk_ids, "gold_chunks": q["evidence_chunks"],
            "page_hit": page_hit, "cited": cited, "answered_well": well,
            "citations": marks,
            "retrieval": scores,
            "answerable_gold": q["answerable"], "faithful": verdict["faithful"],
            "faithful_reason": verdict["reason"],
            "judge_disagreed": verdict.get("judge_disagreed", False),
            "context_chars": answer.context_chars,
            "dropped_titles": answer.dropped_titles,
            "oracle": oracle,
        })
        mark = "○" if page_hit is not False else "×"
        evidence = f" 根拠{scores['found']}/{scores['gold']}" if scores else ""
        print(f"  {mark} {q['id']}{evidence} 文脈{answer.context_chars:>6,}字 "
              f"{'出典○' if cited else '出典×'} {'忠実○' if verdict['faithful'] else '忠実×'} "
              f"{' / '.join(answer.pages)}")

    return records, stats, per_type


def main() -> None:
    args = parse_args()
    questions = json.loads(GOLDEN.read_text(encoding="utf-8"))["questions"]
    if args.ids:
        wanted = [item.strip() for item in args.ids.split(",") if item.strip()]
        by_id = {question["id"]: question for question in questions}
        missing = [question_id for question_id in wanted if question_id not in by_id]
        if missing:
            raise SystemExit(f"存在しない設問IDです: {', '.join(missing)}")
        # 指定順を維持する。q32→q33のような会話回帰を読みやすい順で出すため。
        questions = [by_id[question_id] for question_id in wanted]
    if not questions:
        raise SystemExit("測定対象の設問がありません")
    load_dotenv()
    llm = make_llm()
    print(f'モデル: {llm.name()}')
    pipeline = Pipeline(Path("data/index.json"), Path("data/toc.md"), llm)

    runs = []
    for i in range(args.repeats):
        if args.repeats > 1:
            print(f"\n--- {i + 1}回目 / {args.repeats} ---")
        runs.append(measure(pipeline, llm, questions, oracle=args.oracle))

    # 出力は全実行分を残す。1回目だけ見て判断しないため
    all_records = [dict(r, run=i + 1) for i, (records, _, _) in enumerate(runs) for r in records]
    args.output.write_text(json.dumps(all_records, ensure_ascii=False, indent=2), encoding="utf-8")

    n = len(questions)
    ps = runs[0][1]["page_scored"]

    def summarize(label: str, key: str, denom: int) -> str:
        """反復したときは、値そのものではなく**範囲**を主に出す。

        1回ずつの実行を比べて改善と読むのを、何度もやってしまった
        （M25・M26・M27で「回答の出し方」が74.3〜88.6%の幅で動いた）。
        範囲が重なっているなら、その差はばらつきと区別できない。
        """
        values = [stats[key] for _, stats, _ in runs]
        if len(values) == 1:
            return f"  {label}: {values[0]}/{denom} = {values[0] / denom * 100:.1f}%"
        lo, hi = min(values), max(values)
        mid = sorted(values)[len(values) // 2]
        each = " ".join(str(v) for v in values)
        return (f"  {label}: 中央値 {mid}/{denom} = {mid / denom * 100:.1f}%"
                f"  範囲 {lo}〜{hi}（{lo / denom * 100:.1f}〜{hi / denom * 100:.1f}%）  各回 {each}")

    title = ("Oracle-context: 生成だけの品質（" if args.oracle
             else "M2b: エンドツーエンドの回答品質（") + f"{n}問"
    title += f" × {args.repeats}回）" if args.repeats > 1 else "）"
    print(f"\n{'=' * 78}\n{title}\n{'=' * 78}")
    print("【ルールベース（決定的）】")
    print(summarize("ページ選択が的中  ", "page_hit", ps))
    print(summarize("本文に資料番号     ", "cited", n))
    marks = sum(stats["marks_in_range"] for _, stats, _ in runs)
    outside = sum(stats["out_of_range"] for _, stats, _ in runs)
    listed = sum(stats["source_list"] for _, stats, _ in runs)
    print(f"  資料番号: 実在 {marks}個 / **範囲外 {outside}個**（0であるべき）")
    print(f"  出典一覧を作ってしまった回答: {listed}件（本番では規則違反。0であるべき）")
    print(summarize("回答の出し方が適切", "answered_well", n))
    if not args.oracle:
        cs = runs[0][1]["chunk_scored"]
        if cs:
            print(summarize("根拠の節を1件でも  ", "evidence_hit", cs))
            print(summarize("根拠の節を全部    ", "all_evidence", cs))
            for label, num, den in (
                ("Evidence Recall（節）", "found_total", "gold_total"),
                ("Context Precision  ", "found_total", "picked_total"),
            ):
                values = [
                    (stats[num] / stats[den] * 100) if stats[den] else 0.0
                    for _, stats, _ in runs
                ]
                each = " ".join(f"{v:.1f}%" for v in values)
                print(f"  {label}: 中央値 {sorted(values)[len(values) // 2]:.1f}%"
                      + (f"  範囲 {min(values):.1f}〜{max(values):.1f}%  各回 {each}" if len(values) > 1 else ""))
    dropped = [d for _, stats, _ in runs for d in stats["dropped"]]
    print(f"  照合で落とした架空ページ名: {len(dropped)}件 {dropped or ''}")
    ctx = sorted(r["context_chars"] for r in all_records)
    print(f"  文脈量            : 中央値 {ctx[len(ctx) // 2]:,}字 / 最大 {max(ctx):,}字")

    print("\n【LLM-as-a-Judge（⚠️ 判定器と人手評価の一致度は未測定。確定値にしない）】")
    print(summarize("Faithfulness      ", "faithful", n))
    disagreed = sum(stats["judge_disagreed"] for _, stats, _ in runs)
    print(f"  判定器の自己申告との食い違い: {disagreed}件"
          "（faithfulと言いながら裏付けの無い一文を挙げた、またはその逆）")
    print("  校正: python eval/judge_calibration.py（人手ラベルが要る）")

    print("\n種別ごと（ページ的中 / 忠実性。反復時は合計）:")
    totals: dict[str, dict[str, int]] = {}
    for _, _, per_type in runs:
        for qtype, v in per_type.items():
            t = totals.setdefault(qtype, {"n": 0, "page_hit": 0, "faithful": 0})
            for key in t:
                t[key] += v[key]
    for qtype, v in sorted(totals.items()):
        print(f"  {qtype:<14} {v['page_hit']:>3}/{v['n']:<3}  {v['faithful']:>3}/{v['n']}")

    if args.repeats == 1:
        print("\n⚠️ 1回だけの実行です。前回との差を改善と読まないでください"
              "（--repeats 3 で範囲を見てから判断する）。")

    print(f"\nLLM呼び出し {llm.calls}回 / 合計 {llm.seconds:.0f}秒")
    print(f"回答全文は {args.output} に保存（人手レビュー用。run列で実行回を区別）")


if __name__ == "__main__":
    main()
