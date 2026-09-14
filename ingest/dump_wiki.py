"""WASA Wiki を API 経由で丸ごとローカルに落とし、規模を実測する。

前提: .env に WIKI_API / WIKI_USER / WIKI_PASS を設定済み。

  python -m venv .venv && source .venv/bin/activate
  pip install requests python-dotenv
  python dump_wiki.py

出力:
  dump/pages.jsonl   1行1ページ（wikitext 本文つき）
  dump/images.json   添付ファイル一覧（メタデータのみ、実体は落とさない）
  標準出力に規模サマリ
"""

from __future__ import annotations

import json
import os
import sys
import time
from pathlib import Path

import requests
from dotenv import load_dotenv

load_dotenv()

API = os.environ["WIKI_API"]
USER = os.environ["WIKI_USER"]
PASSWORD = os.environ["WIKI_PASS"]

OUT = Path("dump")
# さくらのレンタルサーバ相手なので、リクエスト間隔を空けて負荷をかけない
DELAY_SEC = 1.0
# revisions は通常アカウントで確実に通る50ページずつ取得する
BATCH = 50

session = requests.Session()
session.headers["User-Agent"] = "WASAWikiDump/0.1 (internal handover-doc chatbot)"


def call(params: dict, method: str = "GET") -> dict:
    """API を1回叩く。MediaWiki のエラーは例外に変換する。"""
    params = {**params, "format": "json", "formatversion": "2"}
    if method == "POST":
        response = session.post(API, data=params, timeout=60)
    else:
        response = session.get(API, params=params, timeout=60)
    response.raise_for_status()
    payload = response.json()
    if "error" in payload:
        raise RuntimeError(f"{payload['error']['code']}: {payload['error'].get('info')}")
    time.sleep(DELAY_SEC)
    return payload


def login() -> str:
    """WASA Wikiの通常アカウントをclientloginで認証する。"""
    token = call({"action": "query", "meta": "tokens", "type": "login"})
    token = token["query"]["tokens"]["logintoken"]

    result = call(
        {
            "action": "clientlogin",
            "username": USER,
            "password": PASSWORD,
            "loginreturnurl": "https://example.org/",  # 使われないが必須
            "logintoken": token,
        },
        method="POST",
    )["clientlogin"]
    ok, reason = result["status"] == "PASS", result.get("message", "")

    if not ok:
        raise SystemExit(f"ログイン失敗: {reason}")

    who = call({"action": "query", "meta": "userinfo"})["query"]["userinfo"]
    if who.get("anon") is not None or who["id"] == 0:
        raise SystemExit("認証は通ったがセッションが匿名のままです。")
    return who["name"]


def content_namespaces() -> dict[int, str]:
    """本文が入りうる名前空間だけ拾う（トークページ・特別ページは除外）。"""
    data = call({"action": "query", "meta": "siteinfo", "siprop": "namespaces"})
    namespaces = {}
    for ns in data["query"]["namespaces"].values():
        ns_id = ns["id"]
        if ns_id < 0 or ns_id % 2 == 1:  # 負=特別系, 奇数=トークページ
            continue
        namespaces[ns_id] = ns.get("name") or "(標準)"
    return namespaces


def list_pages(ns_id: int) -> list[dict]:
    pages, cont = [], {}
    while True:
        data = call(
            {
                "action": "query",
                "list": "allpages",
                "apnamespace": ns_id,
                "aplimit": "max",
                **cont,
            }
        )
        pages.extend(data["query"]["allpages"])
        if "continue" not in data:
            return pages
        cont = data["continue"]


def fetch_contents(page_ids: list[int]) -> list[dict]:
    """wikitext 本文・最終更新日時・版IDをまとめて取得する。

    revid は差分更新の土台になる。次回ダンプ時に revid が変わっていない
    ページは、本文の再取得も要約の再生成もスキップできる。
    """
    out = []
    for i in range(0, len(page_ids), BATCH):
        chunk = page_ids[i : i + BATCH]
        data = call(
            {
                "action": "query",
                "pageids": "|".join(map(str, chunk)),
                "prop": "revisions",
                "rvprop": "ids|content|timestamp|user",
                "rvslots": "main",
            }
        )
        for page in data["query"]["pages"]:
            revisions = page.get("revisions")
            if not revisions:  # 削除済み等
                continue
            revision = revisions[0]
            out.append(
                {
                    "pageid": page["pageid"],
                    "ns": page["ns"],
                    "title": page["title"],
                    "revid": revision.get("revid"),
                    "last_edited": revision["timestamp"],
                    "last_editor": revision.get("user"),
                    "wikitext": revision["slots"]["main"].get("content", ""),
                }
            )
        print(f"  本文取得 {min(i + BATCH, len(page_ids))}/{len(page_ids)}", flush=True)
    return out


def list_images() -> list[dict]:
    images, cont = [], {}
    while True:
        data = call(
            {
                "action": "query",
                "list": "allimages",
                "ailimit": "max",
                "aiprop": "url|size|mime|timestamp",
                **cont,
            }
        )
        images.extend(data["query"]["allimages"])
        if "continue" not in data:
            return images
        cont = data["continue"]


def main() -> None:
    OUT.mkdir(exist_ok=True)

    print(f"ログイン中: {USER}")
    print(f"  → {login()} として認証成功\n")

    namespaces = content_namespaces()
    all_pages = []
    for ns_id, name in sorted(namespaces.items()):
        pages = list_pages(ns_id)
        if pages:
            print(f"名前空間 {ns_id} 「{name}」: {len(pages)} ページ")
            all_pages.extend(pages)
    print(f"\n合計 {len(all_pages)} ページ。本文を取得します…")

    documents = fetch_contents([p["pageid"] for p in all_pages])
    with (OUT / "pages.jsonl").open("w", encoding="utf-8") as f:
        for doc in documents:
            f.write(json.dumps(doc, ensure_ascii=False) + "\n")

    print("\n添付ファイルを列挙中…")
    images = list_images()
    (OUT / "images.json").write_text(
        json.dumps(images, ensure_ascii=False, indent=2), encoding="utf-8"
    )

    # ---- 規模サマリ ----
    total_chars = sum(len(d["wikitext"]) for d in documents)
    by_mime: dict[str, int] = {}
    for image in images:
        by_mime[image.get("mime", "?")] = by_mime.get(image.get("mime", "?"), 0) + 1

    print("\n" + "=" * 52)
    print(f"ページ数        : {len(documents):,}")
    print(f"総文字数        : {total_chars:,} 字")
    if documents:
        print(f"平均            : {total_chars // len(documents):,} 字/ページ")
    # 日本語はおおよそ 1〜1.5 字/トークン。正確な値は後で count_tokens API で測る
    print(f"推定トークン数  : {total_chars:,} 〜 {int(total_chars / 1.5):,}")
    print(f"添付ファイル数  : {len(images):,}")
    for mime, count in sorted(by_mime.items(), key=lambda kv: -kv[1])[:8]:
        print(f"    {mime:<40} {count:>5}")
    print("=" * 52)
    if total_chars < 900_000:
        print("→ 全文が 100 万トークンに収まる見込み。案A（全文コンテキスト）が有力です。")
    else:
        print("→ 全文投入は厳しい規模。案C（ベクトル検索）の検討が要ります。")


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(130)
