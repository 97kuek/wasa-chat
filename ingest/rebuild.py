"""Wikiと公式サイトの取得から索引の検査までを、一つのコマンドで実行する。

    python ingest/rebuild.py
    python ingest/rebuild.py --skip-eval  # 評価だけ省く
    python ingest/rebuild.py --dry-run    # 実行内容だけ確認する

各取得スクリプトが持つ1秒間隔は変更しない。途中で失敗したら後続処理を止め、
古い取得結果と新しい取得結果を混ぜた索引をデプロイしない。
"""

from __future__ import annotations

import argparse
import subprocess
import sys


# ⚠️ **取得元をここへ足し忘れない。** 共有ドライブが抜けており、手元で
# 作り直した索引には315件のDrive資料が入らなかった（2026-09-14に発見）。
# そのまま公開すると、本番から共有ドライブが黙って消える。
# 出所を増やすときは tools/auto_update.py の SOURCES も同時に直すこと。
STEPS = (
    ("Wikiを取得", "ingest/dump_wiki.py"),
    ("公式サイトを取得", "ingest/dump_site.py"),
    ("フライトシミュレータのガイドを取得", "ingest/dump_fee.py"),
    ("共有ドライブを取得", "ingest/dump_drive.py"),
    ("検索索引を作成", "ingest/build_index.py"),
    ("目次を作成", "ingest/build_toc.py"),
    ("検索精度を検査", "eval/retrieval_eval.py"),
)


def selected_steps(skip_eval: bool) -> tuple[tuple[str, str], ...]:
    return STEPS[:-1] if skip_eval else STEPS


def main() -> int:
    parser = argparse.ArgumentParser(description="WASA Chatの資料を一括更新します")
    parser.add_argument("--skip-eval", action="store_true", help="検索精度の検査を省きます")
    parser.add_argument("--dry-run", action="store_true", help="実行するコマンドだけ表示します")
    args = parser.parse_args()

    steps = selected_steps(args.skip_eval)
    for number, (label, script) in enumerate(steps, 1):
        command = [sys.executable, script]
        print(f"[{number}/{len(steps)}] {label}: {' '.join(command)}", flush=True)
        if not args.dry_run:
            subprocess.run(command, check=True)
    if args.dry_run:
        print(f"上の{len(steps)}処理を実行します（今回は確認だけで、ファイルを変更していません）。")
    else:
        print("更新処理が完了しました。差分を確認してからデプロイしてください。")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except subprocess.CalledProcessError as error:
        print(f"更新を中止しました（終了コード {error.returncode}）", file=sys.stderr)
        raise SystemExit(error.returncode) from error
