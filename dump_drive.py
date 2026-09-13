"""部の共有ドライブの資料を取得して dump/drive.jsonl に落とす。

なぜ4つ目の出所を足すのか
---------------------------
Wikiの編集権限が無い部員や、そもそもWikiに書かない部員がいる。その人たちの
資料（議事録・設計メモ・報告書）はDriveに溜まっており、いまの索引には1件も
入っていない。「Wikiに書いてください」と頼むより、**置いてある場所を読みに行く**
ほうが実態に合う（2026-09-13にPMが判断）。

誰の権限で読むのか
------------------
⚠️ **サービスアカウントで読む。利用者ごとのOAuthはしない。**

索引は全員が同じものを検索するため、利用者ごとに取り込んでも、索引へ入れた
時点でDriveのファイル別の権限は失われる。権限を保つには「ファイルごとの
閲覧者」「WASAアカウントとGoogleアカウントの対応」「検索時の絞り込み」まで
必要になり、いまの軽いoriginフィルタから大幅に複雑化する（2026-09-13のCodex）。

その代わり、**閲覧の境界は「WASA Chatにログインできるか」になる**。つまり
DriveのACLではなくWikiアカウントが境界である。docs/09 A-10 に記録してある。

したがって:
  - 対象は FOLDER_IDS で明示したフォルダだけ。検索で見える全ファイルではない
  - そのフォルダに「一部の部員だけが見てよい資料」を置かないこと

鍵を作らない
------------
⚠️ **JSONの鍵を発行してSecret Managerへ置かない。** Cloud Run Jobs に
サービスIDを割り当て、Application Default Credentials で呼ぶ。鍵が無ければ
漏れないし、ローテーションも要らない（2026-09-13のCodex）。

手元で動かすときは次で同じ資格情報になる:

    gcloud auth application-default login

取れるもの・取れないもの
------------------------
Googleドキュメント・スライドは files.export でテキストになる。PDFは本文を
そのまま落として抽出する。**以下は取れない**ので、索引にも入らない:

  - スプレッドシート（CSVエクスポートは先頭シートだけ。複数シートを扱うなら
    XLSXの解析が要るので、要ると分かってから足す）
  - 画像だけのPDF（OCRが要る）
  - スライドの発表者ノート、図の中の文字、コメント

**取れなかったものは黙って捨てず、最後に件数と理由を出す。** 「入っているはず
なのに答えない」を、後から追える形にしておく（docs/01 §5-1）。

使い方
------
    DRIVE_FOLDER_IDS=<フォルダID>,<フォルダID> python dump_drive.py
出力: dump/drive.jsonl（部内資料を含むため .gitignore 対象）
"""

from __future__ import annotations

import io
import json
import os
import re
import sys
import time
from pathlib import Path

OUT = Path("dump/drive.jsonl")
DELAY = 0.2  # Drive APIへの連投を避ける。1分あたりの上限に余裕をもたせる

# 本文がこれ未満のファイルは索引に入れない。空のドキュメントが目次を埋めるため。
# build_index.py の MIN_INDEX_CHARS と揃えている
MIN_CHARS = 20

# 1ファイルから取り込む上限。files.export の上限が10MBなので、それより手前で止める。
# 長すぎる資料は先頭だけでも入っていたほうが「ある」ことは分かる
MAX_CHARS = 200_000

# 取り込む種類。**まずここだけ。** スプレッドシートとOCRは、要ると分かってから足す
GOOGLE_DOC = "application/vnd.google-apps.document"
GOOGLE_SLIDE = "application/vnd.google-apps.presentation"
GOOGLE_SHEET = "application/vnd.google-apps.spreadsheet"
GOOGLE_FOLDER = "application/vnd.google-apps.folder"
GOOGLE_SHORTCUT = "application/vnd.google-apps.shortcut"
PDF = "application/pdf"

SUPPORTED = {GOOGLE_DOC: "文書", GOOGLE_SLIDE: "スライド", PDF: "PDF"}

SCOPES = ["https://www.googleapis.com/auth/drive.readonly"]


def folder_ids() -> list[str]:
    raw = os.getenv("DRIVE_FOLDER_IDS", "").strip()
    if not raw:
        sys.exit("DRIVE_FOLDER_IDS を設定してください（カンマ区切りのフォルダID）。\n"
                 "⚠️ 検索で見える全ファイルではなく、明示したフォルダだけを対象にします。")
    return [value.strip() for value in raw.split(",") if value.strip()]


def build_service():
    try:
        from google.auth import default
        from googleapiclient.discovery import build
    except ImportError:
        sys.exit("google-api-python-client と google-auth が要ります。\n"
                 "  python3 -m pip install google-api-python-client google-auth")
    credentials, _ = default(scopes=SCOPES)
    # cache_discovery=False にしないと、書き込めない環境で警告が出る
    return build("drive", "v3", credentials=credentials, cache_discovery=False)


def check_access(service, folder_id: str) -> str:
    """フォルダを読めるか確かめ、名前を返す。読めなければ例外。

    ⚠️ **黙って0件にしない。** Drive の `files.list` は、権限が無い親を指定しても
    403ではなく**空の一覧**を返す。そのため共有設定を忘れていると、
    「フォルダは空でした」と同じ見え方になり、原因にたどり着けない
    （2026-09-13に実際にそうなった）。先に1回だけ取りに行って区別する。
    """
    from googleapiclient.errors import HttpError

    try:
        found = service.files().get(
            fileId=folder_id, fields="id,name,mimeType", supportsAllDrives=True,
        ).execute()
    except HttpError as error:
        if error.status_code in (403, 404):
            raise SystemExit(
                f"フォルダ {folder_id} を読めません（{error.status_code}）。\n"
                f"このサービスアカウントを共有相手に追加してください:\n"
                f"  {service_account_email()}"
            ) from error
        raise
    if found.get("mimeType") != GOOGLE_FOLDER:
        raise SystemExit(f"{folder_id} はフォルダではありません（{found.get('mimeType')}）")
    return found.get("name", folder_id)


def service_account_email() -> str:
    """いま動いているサービスアカウントのアドレス。

    ⚠️ **Cloud Run 上では credentials.service_account_email が "default" になる。**
    メタデータサーバーから取った資格情報は、自分の名前を知らないまま使える。
    共有相手に追加してもらうには実際のアドレスが要るので、メタデータサーバーへ
    聞きに行く（2026-09-13に "default" と表示されて分からなくなった）。
    """
    try:
        import urllib.request

        request = urllib.request.Request(
            "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/email",
            headers={"Metadata-Flavor": "Google"},
        )
        with urllib.request.urlopen(request, timeout=3) as response:
            return response.read().decode().strip()
    except Exception:  # noqa: BLE001 — 手元で動かしているときは取れない
        pass
    try:
        from google.auth import default

        credentials, _ = default(scopes=SCOPES)
        email = getattr(credentials, "service_account_email", "")
        if email and email != "default":
            return email
    except Exception:  # noqa: BLE001
        pass
    return "（このJobのサービスアカウント）"


def walk(service, folder_id: str, seen: set[str]) -> list[dict]:
    """フォルダの中を再帰的に辿る。ショートカットは実体へ寄せる。"""
    found: list[dict] = []
    stack = [folder_id]
    while stack:
        current = stack.pop()
        if current in seen:
            continue  # 同じフォルダを2回辿らない（ショートカットで輪になり得る）
        seen.add(current)

        token = None
        while True:
            response = service.files().list(
                q=f"'{current}' in parents and trashed = false",
                # 共有ドライブのファイルは、この2つを付けないと見えない
                supportsAllDrives=True,
                includeItemsFromAllDrives=True,
                fields="nextPageToken, files(id, name, mimeType, modifiedTime, "
                       "webViewLink, shortcutDetails)",
                pageSize=100,
                pageToken=token,
            ).execute()
            for entry in response.get("files", []):
                if entry["mimeType"] == GOOGLE_SHORTCUT:
                    details = entry.get("shortcutDetails") or {}
                    target = details.get("targetId")
                    if details.get("targetMimeType") == GOOGLE_FOLDER and target:
                        stack.append(target)
                    continue  # ショートカットの実体は、元のフォルダを辿るときに拾う
                if entry["mimeType"] == GOOGLE_FOLDER:
                    stack.append(entry["id"])
                    continue
                found.append(entry)
            token = response.get("nextPageToken")
            if not token:
                break
            time.sleep(DELAY)
    return found


def export_text(service, entry: dict) -> str:
    """ファイルの本文をテキストで取る。取れない形式は空文字を返す。"""
    mime = entry["mimeType"]
    if mime in (GOOGLE_DOC, GOOGLE_SLIDE):
        data = service.files().export(fileId=entry["id"], mimeType="text/plain").execute()
        return data.decode("utf-8", errors="replace") if isinstance(data, bytes) else str(data)
    if mime == PDF:
        return pdf_text(service, entry)
    return ""


def pdf_text(service, entry: dict) -> str:
    """PDFを落としてテキストを抜く。画像だけのPDFは空になる（OCRはしない）。"""
    try:
        from pypdf import PdfReader
    except ImportError:
        return ""
    data = service.files().get_media(fileId=entry["id"], supportsAllDrives=True).execute()
    try:
        reader = PdfReader(io.BytesIO(data))
        return "\n\n".join(page.extract_text() or "" for page in reader.pages)
    except Exception:
        # 壊れたPDF・暗号化されたPDF。件数は最後に出るので黙って消えはしない
        return ""


def tidy(text: str) -> str:
    """エクスポートしたテキストを、Wikiのページと同じ密度に均す。

    Googleドキュメントの text/plain は空行が多く、そのままだと本文の文字数が
    水増しされて、目次の「中身が薄い」判定がずれる。
    """
    text = text.replace("\r\n", "\n").replace("", "\n")
    text = re.sub(r"[ \t]+\n", "\n", text)
    text = re.sub(r"\n{3,}", "\n\n", text)
    return text.strip()[:MAX_CHARS]


def main() -> int:
    service = build_service()
    seen: set[str] = set()
    entries: list[dict] = []
    for folder in folder_ids():
        name = check_access(service, folder)
        found = walk(service, folder, seen)
        print(f"フォルダ「{name}」: {len(found)} 件")
        entries.extend(found)

    # 同じファイルが複数のフォルダから見えることがある。IDで1回にする
    unique = {entry["id"]: entry for entry in entries}
    print(f"{len(unique)} 件のファイルが見つかった")

    records: list[dict] = []
    skipped: dict[str, int] = {}
    for i, entry in enumerate(sorted(unique.values(), key=lambda e: e["name"]), 1):
        mime = entry["mimeType"]
        if mime not in SUPPORTED:
            label = {GOOGLE_SHEET: "スプレッドシート（未対応）"}.get(mime, f"{mime}（未対応）")
            skipped[label] = skipped.get(label, 0) + 1
            continue

        text = tidy(export_text(service, entry))
        if len(text) < MIN_CHARS:
            reason = "PDFから文字を抜けない（画像だけの可能性）" if mime == PDF else "本文がほぼ空"
            skipped[reason] = skipped.get(reason, 0) + 1
            continue

        records.append({
            # ⚠️ **URLではなくファイルIDを安定IDにする。** 改名・移動しても
            # 同じ資料として扱えるようにするため（2026-09-13のCodex）
            "drive_id": entry["id"],
            "url": entry.get("webViewLink", f"https://drive.google.com/file/d/{entry['id']}/view"),
            "title": entry["name"],
            "kind": SUPPORTED[mime],
            "text": text,
            # ⚠️ **modifiedTime を本文の年代として使わない。** 誤字を直しただけで
            # 新しくなる。鮮度は本文中の年代で判断する（docs/01 §5-1 と同じ扱い）
            "last_edited": (entry.get("modifiedTime") or "")[:10],
        })
        if i % 20 == 0:
            print(f"  {i}/{len(unique)}")
        time.sleep(DELAY)

    OUT.parent.mkdir(parents=True, exist_ok=True)
    with OUT.open("w", encoding="utf-8") as f:
        for record in records:
            f.write(json.dumps(record, ensure_ascii=False) + "\n")

    total = sum(len(r["text"]) for r in records)
    print(f"\n{OUT} に {len(records)} ファイル / 計 {total:,} 字を書き出した")
    for kind in SUPPORTED.values():
        group = [r for r in records if r["kind"] == kind]
        if group:
            print(f"  {kind}: {len(group)}件 / 1件平均 {sum(len(r['text']) for r in group) // len(group):,}字")
    # **取れなかったものを黙って捨てない。**「入っているはずなのに答えない」を追えるようにする
    if skipped:
        print("\n取り込まなかったもの:")
        for reason, count in sorted(skipped.items(), key=lambda item: -item[1]):
            print(f"  {reason}: {count}件")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
