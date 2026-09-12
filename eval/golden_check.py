"""ゴールデンデータが、いまの索引と食い違っていないかを調べる。

  python eval/golden_check.py

**送信は0回。** 決定的な照合だけで終わる。

なぜ要るのか
------------
docs/08 の落とし穴9に「ゴールデンデータのevidenceは必ず実データに照合する」とある。
出題時の想定と実データが食い違っていた例が複数あった（「新宿コズミックセンター」→
正しくは「新宿コズミックスポーツセンター」など）。

加えて、**チャンクIDは `p{ページ番号}-c{連番}` で、索引を作り直すとずれ得る。**
ページの増減や並びが変わると、同じIDが別の節を指す。そのまま測ると、
検索が当たっているのに外れと数えたり、その逆が起きる。**数字が動いたときに
まず疑うのは評価器**（測定器の欠陥は6回見つかっている）。

出力にはWiki由来のページ名が含まれるため、貼り付け先に注意すること。
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

GOLDEN = Path("eval/golden.json")
INDEX = Path("data/index.json")


def check(questions: list[dict], index: dict) -> list[str]:
    """食い違いを人が読める1行ずつにして返す。空なら問題なし。"""
    pages = {page["title"]: page for page in index["pages"]}
    chunk_page = {
        chunk["id"]: page["title"] for page in index["pages"] for chunk in page["chunks"]
    }
    aliases = {alias: page["title"] for page in index["pages"] for alias in page.get("aliases", [])}

    problems: list[str] = []
    for question in questions:
        qid = question["id"]
        for title in question.get("evidence_pages", []):
            if title not in pages:
                hint = f"（別名として存在: {aliases[title]}）" if title in aliases else ""
                problems.append(f"{qid}: 正解ページが索引にない「{title}」{hint}")
        for chunk_id in question.get("evidence_chunks", []):
            owner = chunk_page.get(chunk_id)
            if owner is None:
                problems.append(f"{qid}: 正解チャンクが索引にない「{chunk_id}」")
            elif question.get("evidence_pages") and owner not in question["evidence_pages"]:
                # IDがずれると、ここが真っ先に食い違う
                problems.append(
                    f"{qid}: 正解チャンク {chunk_id} は「{owner}」の節だが、"
                    f"正解ページは {question['evidence_pages']} になっている"
                )
        # 全域型（「最も情報が薄い分野は」など）の根拠は**目次そのもの**であって、
        # 特定の節ではない。ここを「正解チャンクが空＝不備」と数えると、
        # 設計どおりの設問を毎回エラーとして出し続けることになる（q31で確認）
        if question.get("answerable") and not question.get("evidence_chunks") \
                and question.get("type") != "global":
            problems.append(f"{qid}: 答えられる設問なのに正解チャンクが空")
    return problems


def main() -> None:
    parser = argparse.ArgumentParser(description="ゴールデンデータと索引の整合を調べる")
    parser.add_argument("--golden", type=Path, default=GOLDEN)
    parser.add_argument("--index", type=Path, default=INDEX)
    args = parser.parse_args()

    for path in (args.golden, args.index):
        if not path.exists():
            raise SystemExit(f"{path} がありません")

    questions = json.loads(args.golden.read_text(encoding="utf-8"))["questions"]
    index = json.loads(args.index.read_text(encoding="utf-8"))
    problems = check(questions, index)

    scored = [q for q in questions if q.get("evidence_chunks")]
    globals_ = [q for q in questions if q.get("type") == "global"]
    print(f"設問 {len(questions)}問（うち検索採点の対象 {len(scored)}問"
          f" / 全域型 {len(globals_)}問は目次が根拠なので対象外）"
          f" / 索引 {len(index['pages'])}ページ")
    if not problems:
        print("食い違いはありません。")
        return
    print(f"\n食い違い {len(problems)}件:")
    for line in problems:
        print(f"  {line}")
    raise SystemExit("ゴールデンデータか索引のどちらかを直してから測定してください")


if __name__ == "__main__":
    main()
