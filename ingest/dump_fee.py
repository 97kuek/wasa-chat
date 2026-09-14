"""FlightEnvironmentEmulator（フライトシミュレータ）のガイドを取得して dump/fee.jsonl に落とす。

なぜ3つ目の出所を足すのか
---------------------------
引き継ぎWikiの「メインページ」と人物ページから、このガイドのURLが参照されている。
つまり部内では既に正式な資料として扱われているが、本文はWikiの外にあるため、
WASA Chatは「URLがある」ことしか答えられなかった。実際、2026-08-09の評価で
判定器がこのURLを扱った際、中身を確認する手段が無かった。

FEEは訓練・試験飛行の準備に関わるので、使い方や設定の質問は今後も出る。

取得方法
--------
robots.txt も sitemap.xml も存在しない（どちらも404）。ただし `/docs` が
全ページの一覧ページになっており、そこから全URLを辿れる。したがって
無差別クロールはせず、**`/docs` に列挙されたページだけ**を取りに行く。

  /docs（一覧） → /wiki/... と紹介ページ

Cloudflare配信で `Last-Modified` を返さないため、ページ単位の更新日は取れない。
`last_edited` は空にし、鮮度は本文中の年代（build_index.py の extract_era）に任せる。

礼儀
----
リクエスト間隔1秒を縮めないこと。Wiki側（dump_wiki.py）・公式サイト側
（dump_site.py）と同じ方針。robots.txt が無いので明示の許可も禁止も無いが、
出典リンク付きで参照する用途に限り、学習には使わない。
"""

from __future__ import annotations

import html
import json
import re
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

SITE = "https://flight-environment-emulator-guide.makotoyoshida.chatgpt.site"
INDEX_PATH = "/docs"  # 全ページの一覧。ここに出ないURLは取りに行かない
OUT = Path("dump/fee.jsonl")
DELAY = 1.0  # 秒。縮めないこと
# HTTPヘッダは latin-1 しか通らないのでASCIIで書く
UA = "WASAChat/0.1 (internal handover chatbot for WASA; cites with source links)"
MIN_CHARS = 80  # これ未満は目次を汚すだけなので落とす

# 取りに行かないパス。資産と、本文を持たない索引ページ
SKIP_PREFIXES = ("/assets", "/media", "/favicon")

# 一覧には出るが、引き継ぎ資料としての中身が無いページ。
#   /updates                  … dependabotのコミットログ。目次の節一覧が
#                               「bump eslint from 10.7.0 to 10.8.0」で埋まる
#   /wiki/udp-test-...dgram   … node_modules のREADMEが自動同期されたもの。
#                               中身はnpmの定型文で、FEEの説明ではない
EXCLUDE_PATHS = {"/updates", "/wiki/udp-test-node-js-node-modules-dgram"}

# 全ページ共通のナビゲーション。本文に混ざるので落とす
NAV_NOISE = {
    "FlightEnvironmentEmulator ガイド", "トップ", "紹介", "機能", "はじめる",
    "使い方", "ドキュメント", "更新履歴", "GitHub", "ページを開く", "SCROLL",
    "前のページ", "次のページ", "目次", "このページの内容",
}


def fetch(url: str, retries: int = 3) -> str:
    req = urllib.request.Request(url, headers={"User-Agent": UA})
    for attempt in range(retries):
        try:
            with urllib.request.urlopen(req, timeout=30) as res:
                return res.read().decode("utf-8", errors="replace")
        except urllib.error.HTTPError as e:
            if e.code in (429, 500, 502, 503) and attempt < retries - 1:
                time.sleep(5 * (attempt + 1))
                continue
            raise
        except urllib.error.URLError:
            if attempt < retries - 1:
                time.sleep(5 * (attempt + 1))
                continue
            raise
    raise RuntimeError(f"取得できなかった: {url}")


def page_paths(doc: str) -> list[str]:
    """一覧ページから、取得対象のページパスを順序を保って集める。"""
    out: list[str] = []
    for href in re.findall(r'href="(/[^"#?]*)"', doc):
        href = href.rstrip("/") or "/"
        if href.startswith(SKIP_PREFIXES) or href in EXCLUDE_PATHS or href in out:
            continue
        out.append(href)
    return out


def page_title(doc: str) -> str:
    m = re.search(r"(?is)<title>(.*?)</title>", doc)
    if not m:
        return ""
    t = html.unescape(re.sub(r"\s+", " ", m.group(1))).strip()
    # 「概要 | FlightEnvironmentEmulator」からサイト名を落とす
    return re.sub(r"\s*[|｜-]\s*FlightEnvironmentEmulator.*$", "", t).strip()


def extract_text(doc: str) -> str:
    """HTMLから本文を取り出す。見出しは wikitext と同じ「== 見出し ==」で残す。

    build_index.py の split_sections がこの形を節の区切りとして読むため、
    Wiki本文と同じ経路でチャンク化・パンくず付与ができる。
    """
    doc = re.sub(r"(?is)<!--.*?-->", " ", doc)
    doc = re.sub(r"(?is)<(script|style|noscript|svg|form|iframe|template)[^>]*>.*?</\1>", " ", doc)
    doc = re.sub(r"(?is)<(header|footer|nav|aside)\b[^>]*>.*?</\1>", " ", doc)

    def heading(m: re.Match) -> str:
        inner = re.sub(r"(?s)<[^>]+>", " ", m.group("text"))
        inner = re.sub(r"[\s\xa0]+", " ", html.unescape(inner)).strip()
        return f"\n\n== {inner} ==\n" if inner else "\n"

    doc = re.sub(r"(?is)<h([1-4])[^>]*>(?P<text>.*?)</h\1>", heading, doc)
    doc = re.sub(r"(?is)<li[^>]*>", "\n- ", doc)
    doc = re.sub(r"(?is)<br\s*/?>", "\n", doc)
    doc = re.sub(r"(?is)</(p|div|li|tr|td|h[1-6]|pre|code)>", "\n", doc)
    doc = re.sub(r"(?s)<[^>]+>", "", doc)
    doc = html.unescape(doc)

    lines: list[str] = []
    for line in doc.split("\n"):
        line = re.sub(r"[ \t　\xa0]+", " ", line).strip()
        if not line or line in NAV_NOISE:
            continue
        # 中身の無い箇条書き。ナビの <li><a>…</a></li> から記号だけが残る
        if line in {"-", "・", "|"}:
            continue
        # 見出しがナビと本文で重複する。同じ行が続いたら1回にまとめる
        if lines and lines[-1] == line:
            continue
        lines.append(line)
    return "\n".join(lines).strip()


def drop_title_line(records: list[dict]) -> None:
    """本文の冒頭に出るページ名の繰り返しを落とす。

    どのページも「<title>と同じ行」→「サイト名」→「見出し」の順で始まる。
    ページ名は index.json 側がタイトルとして別に持つので、本文には要らない。
    """
    for r in records:
        lines = r["text"].split("\n")
        while lines:
            head = lines[0].strip()
            if head.startswith("=="):
                break
            if head == r["title"] or head.startswith(f"{r['title']} |") \
                    or head.startswith(f"{r['title']} FlightEnvironmentEmulator"):
                lines.pop(0)
                continue
            break
        r["text"] = "\n".join(lines).strip()


def main() -> None:
    OUT.parent.mkdir(exist_ok=True)

    print(f"一覧を取得: {SITE}{INDEX_PATH}")
    paths = page_paths(fetch(SITE + INDEX_PATH))
    time.sleep(DELAY)
    print(f"対象 {len(paths)} ページ")

    records: list[dict] = []
    for i, path in enumerate(paths, 1):
        url = SITE + ("" if path == "/" else path)
        try:
            doc = fetch(url)
        except Exception as e:  # noqa: BLE001 — 1ページ落ちても全体は続ける
            print(f"  取得失敗 {url}: {e}", file=sys.stderr)
            time.sleep(DELAY)
            continue
        title = page_title(doc) or path
        # 一覧・紹介ページとガイド本体で粒度が違うので、目次側で分けられるようにする
        kind = "ガイド" if path.startswith("/wiki/") else "紹介"
        records.append({"url": url, "title": title, "kind": kind,
                        "text": extract_text(doc), "last_edited": ""})
        if i % 10 == 0 or i == len(paths):
            print(f"  {i}/{len(paths)}")
        time.sleep(DELAY)

    drop_title_line(records)

    kept = [r for r in records if len(r["text"]) >= MIN_CHARS]
    dropped = len(records) - len(kept)

    with OUT.open("w", encoding="utf-8") as f:
        for r in kept:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")

    total = sum(len(r["text"]) for r in kept)
    print(f"\n{OUT} に {len(kept)} ページ / 計 {total:,} 字を書き出した"
          f"（本文{MIN_CHARS}字未満の{dropped}ページは除外）")
    for kind in ("紹介", "ガイド"):
        group = [r for r in kept if r["kind"] == kind]
        if group:
            print(f"  {kind}: {len(group)}ページ / "
                  f"1件平均 {total and sum(len(r['text']) for r in group) // len(group):,}字")


if __name__ == "__main__":
    main()
