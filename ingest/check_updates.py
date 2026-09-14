"""公開元がローカル取得後に変わったかを、本文を再取得せず確認する。

    python check_updates.py

終了コード0は変更なし、2は変更あり、1は確認失敗。自動デプロイは行わない。
非公開Wikiの認証情報を外部サービスへ預けず、管理者が更新内容を確認できるためである。
"""

from __future__ import annotations

import json
import os
import sys
import time
from pathlib import Path

import requests
from dotenv import load_dotenv

import dump_fee
import dump_site


WIKI_DUMP = Path("dump/pages.jsonl")
SITE_DUMP = Path("dump/site.jsonl")
FEE_DUMP = Path("dump/fee.jsonl")
DELAY_SEC = 1.0  # さくらのサーバーへ負荷をかけない。縮めないこと


class WikiClient:
    def __init__(self, api: str, user: str, password: str) -> None:
        self.api = api
        self.user = user
        self.password = password
        self.session = requests.Session()
        self.session.headers["User-Agent"] = "WASAChatUpdateCheck/0.1 (internal handover chatbot)"

    def call(self, params: dict, method: str = "GET") -> dict:
        params = {**params, "format": "json", "formatversion": "2"}
        response = self.session.post(self.api, data=params, timeout=60) if method == "POST" \
            else self.session.get(self.api, params=params, timeout=60)
        response.raise_for_status()
        payload = response.json()
        if "error" in payload:
            raise RuntimeError(payload["error"].get("info", payload["error"]["code"]))
        time.sleep(DELAY_SEC)
        return payload

    def login(self) -> None:
        token = self.call({"action": "query", "meta": "tokens", "type": "login"})
        result = self.call({
            "action": "clientlogin", "username": self.user, "password": self.password,
            "loginreturnurl": "https://example.org/",
            "logintoken": token["query"]["tokens"]["logintoken"],
        }, method="POST")["clientlogin"]
        if result["status"] != "PASS":
            raise RuntimeError("Wikiへログインできませんでした")

    def revisions(self) -> dict[int, tuple[str, int]]:
        pages: dict[int, tuple[str, int]] = {}
        continuation: dict = {}
        while True:
            payload = self.call({
                "action": "query", "generator": "allpages", "gapnamespace": 0,
                "gaplimit": "max", "prop": "revisions", "rvprop": "ids", **continuation,
            })
            for page in payload["query"]["pages"]:
                revisions = page.get("revisions") or []
                if revisions:
                    pages[page["pageid"]] = (page["title"], revisions[0]["revid"])
            if "continue" not in payload:
                return pages
            continuation = payload["continue"]


def load_wiki_revisions() -> dict[int, tuple[str, int]]:
    return {
        row["pageid"]: (row["title"], row["revid"])
        for row in (json.loads(line) for line in WIKI_DUMP.open(encoding="utf-8"))
        if row["ns"] == 0
    }


def load_site_dates() -> dict[str, str]:
    return {
        row["url"]: row.get("last_edited", "")[:10]
        for row in (json.loads(line) for line in SITE_DUMP.open(encoding="utf-8"))
    }


def site_dates() -> dict[str, str]:
    dates: dict[str, str] = {}
    for sitemap in dump_site.SITEMAPS:
        dates.update(dump_site.sitemap_entries(sitemap))
        time.sleep(dump_site.DELAY)
    return dates


def load_fee_pages() -> dict[str, str]:
    """取得済みガイドの、ページURLと本文の長さ。

    このサイトは Last-Modified を返さないため、更新日では比べられない。
    一覧に載るページの顔ぶれと、各ページの本文量の変化で「変わったか」を見る。
    """
    if not FEE_DUMP.exists():
        return {}
    return {
        row["url"]: str(len(row["text"]))
        for row in (json.loads(line) for line in FEE_DUMP.open(encoding="utf-8"))
    }


def fee_pages() -> dict[str, str]:
    paths = dump_fee.page_paths(dump_fee.fetch(dump_fee.SITE + dump_fee.INDEX_PATH))
    time.sleep(dump_fee.DELAY)
    out: dict[str, str] = {}
    for path in paths:
        url = dump_fee.SITE + ("" if path == "/" else path)
        out[url] = str(len(dump_fee.extract_text(dump_fee.fetch(url))))
        time.sleep(dump_fee.DELAY)
    return out


def describe_changes(label: str, local: dict, remote: dict) -> bool:
    added = remote.keys() - local.keys()
    removed = local.keys() - remote.keys()
    changed = {key for key in local.keys() & remote.keys() if local[key] != remote[key]}
    print(f"{label}: 追加 {len(added)} / 更新 {len(changed)} / 削除 {len(removed)}")
    return bool(added or removed or changed)


def main() -> int:
    load_dotenv()
    required = ("WIKI_API", "WIKI_USER", "WIKI_PASS")
    missing = [key for key in required if not os.environ.get(key)]
    if missing:
        raise RuntimeError(".envに設定が必要です: " + ", ".join(missing))
    if not WIKI_DUMP.exists() or not SITE_DUMP.exists():
        raise RuntimeError("先に python rebuild.py を実行して取得データを作ってください")

    wiki = WikiClient(*(os.environ[key] for key in required))
    wiki.login()
    changed = describe_changes("Wiki", load_wiki_revisions(), wiki.revisions())
    changed = describe_changes("公式サイト", load_site_dates(), site_dates()) or changed
    if FEE_DUMP.exists():
        changed = describe_changes("フライトシミュレータ", load_fee_pages(), fee_pages()) or changed
    if changed:
        print("公開元に変更があります。python rebuild.py で内容を確認してください。")
        return 2
    print("公開元の変更はありません。")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:  # noqa: BLE001 — 定期実行時も短い失敗理由を残す
        print(f"更新確認に失敗しました: {error}", file=sys.stderr)
        raise SystemExit(1) from error
