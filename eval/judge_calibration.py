"""LLM判定器（Faithfulness）を人手評価で校正する。

設計方針 docs/01-設計方針.md §5-3「判定器そのものの検証」に対応する。

**判定器を無条件に信じないための道具である。** Faithfulness の自動値は、
人手との一致度（Cohen's κ）と false-accept 率が分かるまで確定指標にしない、
というのが決めてあることで、これまでその2つを出す手段が無かった。

使い方:

  1. `python eval/answer_eval.py --repeats 3` で eval/answers.json を作る
  2. 人が20問ほど読み、eval/human_labels.json を書く

     {"q01": {"faithful": true, "note": "資料どおり"},
      "q18": {"faithful": false, "note": "翼型を資料外の値で書いている"}}

  3. `python eval/judge_calibration.py`

⚠️ **false-accept（誤りを正しいと判定する）を最重視する。** 引き継ぎ資料では
「答えられない」より「間違って答える」ほうがはるかに有害なので、
見逃しと空振りを同じ重みで見てはいけない（docs/01 §5-3）。

出力にはWiki由来の本文が混ざるため、結果を貼り付ける先に注意すること。
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

ANSWERS = Path("eval/answers.json")
LABELS = Path("eval/human_labels.json")


def cohens_kappa(pairs: list[tuple[bool, bool]]) -> float | None:
    """2値ラベルの一致度。偶然の一致を差し引いた値を返す。

    **単純な一致率を使わない理由**は、faithful が大半を占めるデータでは
    「全部 faithful」と答えるだけの判定器でも9割一致してしまうためである。
    片方が1種類しか出していないときは κ が定義できないので None を返す。
    """
    n = len(pairs)
    if n == 0:
        return None
    agree = sum(1 for human, judge in pairs if human == judge) / n
    human_true = sum(1 for human, _ in pairs if human) / n
    judge_true = sum(1 for _, judge in pairs if judge) / n
    chance = human_true * judge_true + (1 - human_true) * (1 - judge_true)
    if chance == 1:
        return None
    return (agree - chance) / (1 - chance)


def confusion(pairs: list[tuple[bool, bool]]) -> dict[str, int]:
    """人手を正解としたときの混同行列。

    false_accept: 人が「裏付けなし」と見たものを、判定器が faithful と通した数。
    **これが本命の指標である。**
    """
    counts = {"true_accept": 0, "false_accept": 0, "true_reject": 0, "false_reject": 0}
    for human, judge in pairs:
        if human and judge:
            counts["true_accept"] += 1
        elif not human and judge:
            counts["false_accept"] += 1
        elif not human and not judge:
            counts["true_reject"] += 1
        else:
            counts["false_reject"] += 1
    return counts


def collect(records: list[dict], labels: dict[str, dict]) -> tuple[list[tuple[bool, bool]], list[dict]]:
    """人手ラベルのある回答だけを突き合わせる。

    同じ設問を複数回測っていることがあるので、**実行ごとに1件として数える。**
    まとめて多数決にすると、判定器のばらつきそのものが見えなくなる。
    """
    pairs: list[tuple[bool, bool]] = []
    mismatches: list[dict] = []
    for record in records:
        label = labels.get(record["id"])
        if not label or "faithful" not in label:
            continue
        human = bool(label["faithful"])
        judge = bool(record["faithful"])
        pairs.append((human, judge))
        if human != judge:
            mismatches.append({
                "id": record["id"],
                "run": record.get("run"),
                "human": human,
                "judge": judge,
                "note": label.get("note", ""),
                "judge_reason": record.get("faithful_reason", ""),
            })
    return pairs, mismatches


def main() -> None:
    parser = argparse.ArgumentParser(description="Faithfulness判定器を人手評価で校正する")
    parser.add_argument("--answers", type=Path, default=ANSWERS)
    parser.add_argument("--labels", type=Path, default=LABELS)
    args = parser.parse_args()

    if not args.answers.exists():
        raise SystemExit(f"{args.answers} がありません。先に answer_eval.py を実行してください")
    if not args.labels.exists():
        raise SystemExit(
            f"{args.labels} がありません。人が読んだ判定を次の形で置いてください:\n"
            '  {"q01": {"faithful": true, "note": "資料どおり"}}'
        )

    records = json.loads(args.answers.read_text(encoding="utf-8"))
    labels = json.loads(args.labels.read_text(encoding="utf-8"))
    pairs, mismatches = collect(records, labels)
    if not pairs:
        raise SystemExit("突き合わせられる回答がありません（IDが一致していない可能性があります）")

    counts = confusion(pairs)
    kappa = cohens_kappa(pairs)
    judged_ids = sorted({record["id"] for record in records if record["id"] in labels})

    print(f"{'=' * 70}\n判定器の校正（{len(pairs)}件 / 設問{len(judged_ids)}問）\n{'=' * 70}")
    print(f"  一致          : {counts['true_accept'] + counts['true_reject']}/{len(pairs)}")
    print(f"  κ（Cohen）    : {'算出不能（片方が1種類しか出していない）' if kappa is None else f'{kappa:.2f}'}")

    negatives = counts["false_accept"] + counts["true_reject"]
    rate = counts["false_accept"] / negatives * 100 if negatives else 0.0
    print(f"  false-accept  : {counts['false_accept']}/{negatives} = {rate:.1f}%"
          "（人が「裏付けなし」と見たものを判定器が通した割合）")
    print(f"  false-reject  : {counts['false_reject']}件（正しい回答を裏付けなしと言った）")

    if mismatches:
        print("\n食い違った回答:")
        for item in mismatches:
            print(f"  {item['id']}（{item['run']}回目）人手={item['human']} 判定器={item['judge']}")
            if item["note"]:
                print(f"    人手: {item['note']}")
            if item["judge_reason"]:
                print(f"    判定器: {item['judge_reason']}")

    print("\n⚠️ κが低い、または false-accept が残る間は、Faithfulnessの自動値を"
          "確定指標として docs/08 へ書かないこと（docs/01 §5-3）。")


if __name__ == "__main__":
    main()
