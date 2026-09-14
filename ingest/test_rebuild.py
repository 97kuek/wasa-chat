import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(ROOT))

import rebuild  # noqa: E402

from tools import auto_update  # noqa: E402


class RebuildTest(unittest.TestCase):
    def test_skip_eval_only_removes_last_step(self) -> None:
        """--skip-eval は検索検査だけを外す。取得元が増えても崩れないよう、
        件数を直に書かずに関係で確かめる。"""
        full = rebuild.selected_steps(False)
        skipped = rebuild.selected_steps(True)
        self.assertEqual(len(skipped), len(full) - 1)
        self.assertEqual(full[-1][1], "eval/retrieval_eval.py")
        self.assertEqual(skipped[-1][1], "ingest/build_toc.py")
        self.assertEqual(full[:-1], skipped)

    def test_every_source_is_fetched_before_indexing(self) -> None:
        """取得は索引作成より先。順序が崩れると古いデータで索引を作ってしまう。"""
        scripts = [script for _, script in rebuild.selected_steps(False)]
        for fetcher in ("ingest/dump_wiki.py", "ingest/dump_site.py", "ingest/dump_fee.py"):
            self.assertIn(fetcher, scripts)
            self.assertLess(scripts.index(fetcher), scripts.index("ingest/build_index.py"))

    def test_rebuild_fetches_every_source_auto_update_knows(self) -> None:
        """⚠️ **手元の再構築と自動更新で、取得する出所を食い違わせない。**

        rebuild.py に共有ドライブの取得が入っておらず、手元で作り直した索引には
        315件のDrive資料が入らなかった（2026-09-14に発見）。そのまま公開すると
        **本番から共有ドライブが黙って消える**。出所の一覧は auto_update.py の
        SOURCES が正本なので、そちらと突き合わせる。
        """
        expected = {script for _, _, script in auto_update.SOURCES}
        actual = {script for _, script in rebuild.selected_steps(False)}
        missing = expected - actual
        self.assertFalse(
            missing,
            f"ingest/rebuild.py が取得していない出所があります: {sorted(missing)}",
        )

    def test_every_step_points_at_a_real_file(self) -> None:
        """手順に書いたスクリプトが実在すること。移動したときに気づけるようにする。"""
        for label, script in rebuild.selected_steps(False):
            self.assertTrue((ROOT / script).exists(), f"{label}: {script} がありません")


if __name__ == "__main__":
    unittest.main()
