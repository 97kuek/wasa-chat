"""公開元の変更を検知し、変わっていれば索引を作り直して差し替える。

    python tools/auto_update.py              # 変更があれば取り込んで差し替える
    python tools/auto_update.py --check-only # 変更の有無だけ見る（取得しない）
    python tools/auto_update.py --force      # 変更が無くても作り直す

Cloud Run Job から定期実行することを想定している。**状態を持ち回らない。**
比較の基準は手元の `dump/` ではなく、いま公開されている索引そのもの
（`INDEX_GCS` から読む）なので、毎回まっさらな環境で動いてよい。

なぜ自動化してよいことになったか
--------------------------------
docs/07 には自動化しない理由が2つ書いてあった。

  1. 非公開Wikiの認証情報と取得データを、外部のCIサービスへ置かないため
  2. Wikiの誤編集が、そのまま自動で本番の回答に反映される事故を避けるため

⚠️ **2つ目は2026-09-12に人間の判断で受け入れた。** 更新が遅れることのほうが困る、
という判断である。1つ目は変えていないので、**外部CIは使わず、既存のGoogle Cloudの中で
動かす**。Wikiの資格情報はいままでどおり Secret Manager から読む。

止める条件
----------
誤編集は受け入れたが、**取得の失敗まで受け入れたわけではない。**
取得が途中で落ちた索引を上げると、全部の質問へ「資料が見つかりません」と答え続ける
状態になり、しかも壊れていることが画面から分からない。次の場合は差し替えない。

  - ページ数が100件を下回る（publish-index.sh と同じ下限）
  - どれかの出所のページ数が、いまの版から3割以上減っている
  - チャンクが1つも無い
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from collections import Counter
from pathlib import Path

# 取得スクリプトは ingest/ にある。ライブラリとしても import するので通しておく
sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "ingest"))

from dotenv import load_dotenv  # noqa: E402

import check_updates  # noqa: E402
import dump_fee  # noqa: E402
import dump_site  # noqa: E402

ROOT = Path(__file__).resolve().parent.parent
INDEX = ROOT / "data" / "index.json"
TOC = ROOT / "data" / "toc.md"
# 前回の公開元の一覧。**索引と比べてはいけない理由**は remote_snapshot() にある
MANIFEST = ROOT / "data" / "sources.json"
DUMP = ROOT / "dump"
# 取得済みの生データ。**変わっていない出所を取り直さないために持ち回る。**
# 索引と同じ非公開バケットへ置く（中身は索引に入っているものと同じ）
DUMP_FILES = ("pages.jsonl", "site.jsonl", "fee.jsonl", "drive.jsonl", "images.json")

# 生データのファイルと、それを作る出所の対応。
# images.json はWikiの添付一覧なので wiki が作る。
DUMP_OWNER = {
    "pages.jsonl": "wiki",
    "images.json": "wiki",
    "site.jsonl": "site",
    "fee.jsonl": "fee",
    "drive.jsonl": "drive",
}

# 出所と、その取得スクリプト。
#
# ⚠️ **出所を足すときはここだけを直す。** 以前は「全部取り直す」ときの集合を
# 別に書いており、共有ドライブを足したときに**そこだけ更新し忘れて、全部取り直す
# 経路でDriveが黙って飛ばされた**（2026-09-13に本番のログで発覚）。
# 取得の実行も、取り直す範囲の判定も、この表から導く。
SOURCES = (
    ("wiki", "Wikiを取得", "ingest/dump_wiki.py"),
    ("site", "公式サイトを取得", "ingest/dump_site.py"),
    ("fee", "フライトシミュレータのガイドを取得", "ingest/dump_fee.py"),
    ("drive", "共有ドライブを取得", "ingest/dump_drive.py"),
)


def all_sources() -> set[str]:
    """取り直しうる出所。設定していないものは含めない。

    共有ドライブは DRIVE_FOLDER_IDS が要る。未設定のまま取りに行くと、
    ingest/dump_drive.py が「設定してください」で止まり、**Wikiの更新まで公開できなくなる**。
    """
    names = {key for key, _, _ in SOURCES}
    if not os.getenv("DRIVE_FOLDER_IDS", "").strip():
        names.discard("drive")
    return names

# 取得の失敗を公開しないための下限。publish-index.sh と同じ値にすること
MIN_PAGES = 100
# ある出所のページ数がここまで減っていたら、取得が壊れたとみなす
MAX_SHRINK = 0.3


def published(location: str, name: str) -> dict | None:
    """公開中のファイルを読む。無ければ None。"""
    if not location.startswith("gs://"):
        path = Path(location) / name
        return json.loads(path.read_text(encoding="utf-8")) if path.exists() else None

    from google.cloud import storage  # 遅延import。手元では要らない

    bucket_name, _, prefix = location.removeprefix("gs://").partition("/")
    prefix = prefix.strip("/")
    blob = storage.Client().bucket(bucket_name).blob(f"{prefix}/{name}" if prefix else name)
    if not blob.exists():
        return None
    return json.loads(blob.download_as_bytes())


def remote_snapshot() -> dict:
    """公開元がいま返している一覧を取る。**これをそのまま次回の基準として保存する。**

    ⚠️ **取得後に残ったものと比べてはいけない。** 索引にも `dump/` にも、
    公開元が返すページの全部は入らない（リダイレクト、20字未満、本文80字未満の投稿は
    取り込み時に落とす）。母集団が違うものを比べると、**毎回「追加された」「削除された」
    と誤検知する**。実際にその形で作ったところ、Wikiで78件の削除・公式サイトで19件の
    追加を毎回報告し、再構築しても索引は1バイトも変わらなかった
    （2026-09-12に実行して確認）。毎回11分の取得が走り続けることになる。

    比較の基準を「公開元が返したもの」に統一すれば、母集団が一致する。

    ⚠️ **フライトシミュレータのガイドは、一覧ページ1枚しか見ない。**
    このサイトは `Last-Modified` も `ETag` も返さない（2026-09-12に確認）。
    中身の変化まで見るには32ページを全部取りに行くことになり、確認そのものが重くなる。
    一覧ページだけなら1リクエストで済み、**ページの増減は拾える**。
    既存ページの中身が書き換わった場合は拾えないので、そこは `--force` で取り直す。
    """
    client = check_updates.WikiClient(
        os.environ["WIKI_API"], os.environ["WIKI_USER"], os.environ["WIKI_PASS"]
    )
    client.login()
    # client.revisions() は {pageid: (title, revid)}
    wiki = {str(pageid): revid for pageid, (_, revid) in client.revisions().items()}

    site: dict[str, str] = {}
    for sitemap in dump_site.SITEMAPS:
        site.update(dump_site.sitemap_entries(sitemap))

    # 一覧に載っているページの顔ぶれだけ。中身の変化は見ていない
    try:
        paths = dump_fee.page_paths(dump_fee.fetch(dump_fee.SITE + dump_fee.INDEX_PATH))
        fee = {path: "" for path in paths}
    except Exception as error:  # noqa: BLE001 — 第三者サイト。落ちても全体は止めない
        print(f"ガイドの一覧を取れませんでした（変更なしとして続けます）: {error}")
        fee = {}

    # 共有ドライブは modifiedTime を変更の印にする。
    #
    # ⚠️ **これは「中身が新しい」ではなく「触られた」印である。** 誤字を1つ
    # 直しただけでも新しくなるので、再構築の引き金にはしてよいが、
    # 資料の鮮度として使ってはいけない（docs/01 §5-1 と同じ扱い）。
    #
    # DRIVE_FOLDER_IDS が未設定なら共有ドライブは使っていないので、空を返す。
    # 空のまま比べると「全削除」に見えるので、changed_since 側で見ない
    drive = drive_snapshot()
    return {"wiki": wiki, "site": site, "fee": fee, "drive": drive}


def drive_snapshot() -> dict:
    """共有ドライブのファイル一覧（ID → 更新時刻）。使っていなければ空。"""
    if not os.getenv("DRIVE_FOLDER_IDS", "").strip():
        return {}
    try:
        import dump_drive

        service = dump_drive.build_service()
        seen: set[str] = set()
        found: dict[str, str] = {}
        for folder in dump_drive.folder_ids():
            # ⚠️ **読めないフォルダを「変更なし」にしない。** files.list は権限が
            # 無い親でも空を返すので、確かめないと共有設定の忘れに気づけない
            dump_drive.check_access(service, folder)
            for entry in dump_drive.walk(service, folder, seen):
                found[entry["id"]] = entry.get("modifiedTime", "")
        print(f"共有ドライブ: {len(found)} 件のファイルが見えています")
        return found
    except Exception as error:  # noqa: BLE001 — 外部API。落ちても全体は止めない
        print(f"共有ドライブの一覧を取れませんでした（変更なしとして続けます）: {error}")
        return {}


def with_unchanged(previous: dict | None, snapshot: dict, rebuilt: set[str]) -> dict:
    """取り直した出所だけ新しい基準にし、それ以外は前回のまま残す。

    基準と索引の中身がずれると、ずれた出所は変更なしと判断され続けて
    永久に取り込まれない。取り直した範囲だけを進めることでずれを防ぐ。
    """
    merged = dict(snapshot)
    for key, value in (previous or {}).items():
        if key not in rebuilt:
            merged[key] = value
    return merged


def source_counts(index: dict) -> Counter:
    return Counter((page.get("source") or "wiki") for page in index["pages"])


def changed_since(previous: dict, snapshot: dict) -> set[str]:
    """前回の基準と比べて、**どの出所が変わったか**を返す。空なら変更なし。"""
    changed: set[str] = set()
    if check_updates.describe_changes("Wiki", previous.get("wiki", {}), snapshot["wiki"]):
        changed.add("wiki")
    if check_updates.describe_changes("公式サイト", previous.get("site", {}), snapshot["site"]):
        changed.add("site")
    # 一覧が取れなかったときは比べない（空と比べると全削除に見える）
    if snapshot.get("fee") and check_updates.describe_changes(
        "ガイド", previous.get("fee", {}), snapshot["fee"]
    ):
        changed.add("fee")
    if snapshot.get("drive") and check_updates.describe_changes(
        "共有ドライブ", previous.get("drive", {}), snapshot["drive"]
    ):
        changed.add("drive")
    return changed


def rebuild(sources: set[str]) -> None:
    """変わった出所だけ取り直し、索引を作り直す。**途中で失敗したら止める。**

    ⚠️ **取得は出所ごとに分ける。** 全部取り直すと約11分かかるが、その大半は
    公式サイトの500ページを1件ずつ取る時間である（実測）。

      Wiki 117ページ        50件ずつまとめて3リクエスト → 数秒
      公式サイト 500ページ   1ページずつ1秒間隔        → 8〜9分
      ガイド 32ページ        1ページずつ              → 約30秒
      索引作成＋目次                                 → 0.3秒

    Wikiを直しただけで公式サイトを取り直すのは、**待ち時間のほぼ全部が無駄**になる。
    古い取得結果と新しい取得結果を混ぜた索引を作らないため、1つでも失敗したら止める。
    """
    for key, label, script in SOURCES:
        if key not in sources:
            print(f"--- {label}: 変更なしのため省略 ---", flush=True)
            continue
        print(f"--- {label} ---", flush=True)
        subprocess.run([sys.executable, script], cwd=ROOT, check=True)
    for label, script in (("検索索引を作成", "ingest/build_index.py"), ("目次を作成", "ingest/build_toc.py")):
        print(f"--- {label} ---", flush=True)
        subprocess.run([sys.executable, script], cwd=ROOT, check=True)


def safe_to_publish(before: dict | None, after: dict) -> list[str]:
    """差し替えてよいかを調べ、止める理由を返す。空なら公開してよい。"""
    problems: list[str] = []
    pages = after["pages"]
    if len(pages) < MIN_PAGES:
        problems.append(f"ページ数が{len(pages)}件しかありません（{MIN_PAGES}件未満）")
    if sum(len(page["chunks"]) for page in pages) == 0:
        problems.append("チャンクが1つもありません")

    # **ページ数が同じでも、本文を失っていることがある。**
    # 取得が「空の本文」で成功してしまう経路があるため、総量でも見る
    if before:
        old_chars = sum(page.get("chars", 0) for page in before["pages"])
        new_chars = sum(page.get("chars", 0) for page in pages)
        if old_chars and new_chars <= old_chars * (1 - MAX_SHRINK):
            problems.append(f"本文の総量が {old_chars:,} → {new_chars:,} 字へ減りました")
        old_chunks = sum(len(page["chunks"]) for page in before["pages"])
        new_chunks = sum(len(page["chunks"]) for page in pages)
        if old_chunks and new_chunks <= old_chunks * (1 - MAX_SHRINK):
            problems.append(f"チャンク数が {old_chunks} → {new_chunks} へ減りました")

    if before:
        old, new = source_counts(before), source_counts(after)
        for source, old_count in old.items():
            new_count = new.get(source, 0)
            if old_count and new_count <= old_count * (1 - MAX_SHRINK):
                problems.append(
                    f"{source} のページ数が {old_count} → {new_count} へ"
                    f"{int((1 - new_count / old_count) * 100)}%減りました"
                )
    return problems


def pull_dumps(location: str) -> set[str]:
    """前回の取得結果を取り寄せ、**足りなかった出所**を返す。

    ⚠️ **1つ足りないだけで全部取り直さない。** 出所を足した直後は、その出所の
    生データだけが保存されていない。それで「揃わない」と判断すると、Wikiと
    公式サイトを11分かけて取り直すことになる（2026-09-13に実際に起きた）。
    足りないものだけを取り直せばよい。

    どの出所にも属さないファイル（images.json）が欠けたときは、Wikiを取り直す。
    """
    DUMP.mkdir(exist_ok=True)
    missing: set[str] = set()

    def note(name: str) -> None:
        missing.add(DUMP_OWNER.get(name, "wiki"))

    if not location.startswith("gs://"):
        for name in DUMP_FILES:
            if not (DUMP / name).exists():
                note(name)
        return missing

    from google.cloud import storage

    bucket_name, _, prefix = location.removeprefix("gs://").partition("/")
    bucket = storage.Client().bucket(bucket_name)
    prefix = prefix.strip("/")
    for name in DUMP_FILES:
        blob = bucket.blob(f"{prefix}/dump/{name}" if prefix else f"dump/{name}")
        if not blob.exists():
            note(name)
            continue
        blob.download_to_filename(DUMP / name)
    return missing


def publish(location: str) -> None:
    if not location.startswith("gs://"):
        print(f"差し替え先がGCSではありません（{location}）。手元のファイルは更新済みです。")
        return
    from google.cloud import storage

    bucket_name, _, prefix = location.removeprefix("gs://").partition("/")
    bucket = storage.Client().bucket(bucket_name)
    prefix = prefix.strip("/")
    # ⚠️ **index.json を最後に上げること。** 本番は index.json の世代だけを見て
    # 読み直すので、これが先に変わると「新しい本文＋古い目次」を掴み、しかも
    # 新しい世代を記録するため**次の更新まで混ざったまま**になる。
    # index.json を最後に置けば、目次は必ず先に揃っている（2026-09-12にCodexが指摘）。
    for path in (TOC, MANIFEST, INDEX):
        # ⚠️ **無いものは触らない。** `--only` で取り直したときは基準（sources.json）を
        # 進めないので、手元にファイルが無い。公開中のものをそのまま残す
        # （消したり空で上書きしたりすると、次回が全件取り直しになる）
        if not path.exists():
            print(f"据え置き: {path.name}（今回は作り直していません）")
            continue
        name = f"{prefix}/{path.name}" if prefix else path.name
        bucket.blob(name).upload_from_filename(path)
        print(f"差し替え: gs://{bucket_name}/{name}")
    # 次回、変わっていない出所を取り直さないために残す
    for dump_name in DUMP_FILES:
        path = DUMP / dump_name
        if not path.exists():
            continue
        name = f"{prefix}/dump/{dump_name}" if prefix else f"dump/{dump_name}"
        bucket.blob(name).upload_from_filename(path)
    # 本番は世代番号を見ているので、ここで再デプロイは要らない（docs/07）
    print("本番は次の更新確認（既定60秒以内）で読み直します。")


def main() -> int:
    parser = argparse.ArgumentParser(description="公開元の変更を取り込んで索引を差し替えます")
    parser.add_argument("--check-only", action="store_true", help="変更の有無だけ調べます")
    parser.add_argument("--force", action="store_true", help="変更が無くても作り直します")
    # 取り出し方を変えたときは、その出所だけ取り直せば足りる。
    # 全部取り直すと公式サイトの500ページで11分かかる（実測）
    parser.add_argument("--only", default="",
                        help="この出所だけ取り直します（カンマ区切り。例: drive）")
    args = parser.parse_args()

    load_dotenv()
    for key in ("WIKI_API", "WIKI_USER", "WIKI_PASS"):
        if not os.environ.get(key):
            raise SystemExit(f"{key} が未設定です")

    location = os.environ.get("INDEX_GCS") or str(ROOT / "data")
    current = published(location, "index.json")
    previous = published(location, "sources.json")
    snapshot = remote_snapshot()
    every = all_sources()
    if only := {v.strip() for v in args.only.split(",") if v.strip()}:
        if unknown := only - every:
            raise SystemExit(f"知らない出所です: {sorted(unknown)}（使えるのは {sorted(every)}）")
        print(f"指定された出所だけ取り直します: {sorted(only)}")
        # ⚠️ **先に前回の取得結果を取り寄せる。** 索引は全出所そろって作るので、
        # 指定した出所だけ取っても、ほかの生データが無ければ作れない
        if missing := pull_dumps(location) & every:
            print(f"前回の取得結果が無い出所も取り直します: {sorted(missing)}")
            only |= missing
        rebuild(only)
        after = json.loads(INDEX.read_text(encoding="utf-8"))
        if problems := safe_to_publish(current, after):
            print("差し替えを中止します:", file=sys.stderr)
            for problem in problems:
                print(f"  - {problem}", file=sys.stderr)
            return 1
        # **基準（sources.json）は進めない。** 取り直しただけで公開元が
        # 変わったわけではない。publish は手元に無いファイルを飛ばす
        publish(location)
        return 0
    if current is None or previous is None:
        print("公開中の索引か取得一覧がありません。初回として全部取り直します。")
        changed = every
    else:
        print(f"公開中の索引: {len(current['pages'])}ページ（{dict(source_counts(current))}）")
        changed = every if args.force else changed_since(previous, snapshot)

    if not changed:
        print("公開元の変更はありません。何もしません。")
        return 0
    if args.check_only:
        print(f"変更のあった出所: {sorted(changed)}（--check-only のため取得しません）。")
        return 2

    # 前回の取得結果のうち、足りないものは取り直す（全部ではない）
    if missing := pull_dumps(location) & every:
        print(f"前回の取得結果が無い出所: {sorted(missing)}")
        changed = changed | missing
    print(f"取り直す出所: {sorted(changed)}")
    rebuild(changed)
    # **取得の直前に見た公開元**を次回の基準として残す。取得後に残ったものではない。
    #
    # ⚠️ **取り直していない出所は、前回の基準のまま残す。** 索引に入っていないのに
    # 「見た」と記録すると、その出所は**二度と取りに行かれなくなる**。実際に
    # 2026-09-13、共有ドライブが取得対象から漏れたまま基準だけ更新され、
    # 412ファイルが永久に取り込まれない状態になった。
    #
    # 「基準に載っているもの = 索引に入っているもの」を守る。
    MANIFEST.write_text(json.dumps(with_unchanged(previous, snapshot, changed), ensure_ascii=False),
                        encoding="utf-8")
    after = json.loads(INDEX.read_text(encoding="utf-8"))
    problems = safe_to_publish(current, after)
    if problems:
        print("差し替えを中止します:", file=sys.stderr)
        for problem in problems:
            print(f"  - {problem}", file=sys.stderr)
        return 1

    print(f"新しい索引: {len(after['pages'])}ページ（{dict(source_counts(after))}）")
    publish(location)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except subprocess.CalledProcessError as error:
        print(f"取得または索引作成に失敗しました（{error.cmd}）", file=sys.stderr)
        raise SystemExit(1) from error
    except Exception as error:  # noqa: BLE001 — 定期実行なので短い理由をログへ残す
        print(f"自動更新に失敗しました: {error}", file=sys.stderr)
        raise SystemExit(1) from error
