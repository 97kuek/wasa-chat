"""公式サイト（https://wasa-birdman.com/）を取得して dump/site.jsonl に落とす。

なぜWiki以外も取り込むのか
---------------------------
引き継ぎWikiは「作り方」は詳しいが、**サークルそのものの説明**が薄い。
公式サイトにしか無い情報の実例:

  - 設立1965年、早稲田大学と日本女子大学の公認、インカレである、という基礎情報
  - 歴代機体名の一覧（2004 Atyone 〜 2021 Voyager）
  - 年間の活動サイクル（製作開始→夏合宿→学園祭→TF→鳥コン）と各班の役割
  - 2012年以降の試験飛行報告

新入生や外部への説明を求められたとき、Wikiだけでは答えられない。

取得方法
--------
WordPress の REST API は無効化されている（/wp-json/ が404）。sitemap から
URLを集めてHTMLを直に読む。sitemap の <loc> は CDATA で包まれている点に注意。

  sitemap.xml（索引） → post-sitemap.xml / page-sitemap.xml → 各ページ

category-sitemap と post_tag-sitemap は記事一覧ページなので取らない。
本文が無く、同じ見出しの羅列でノイズになるだけである。

礼儀
----
リクエスト間隔1秒を縮めないこと。Wiki側（dump_wiki.py）と同じ方針。
robots.txt の Content-Signal は `use=reference` を許可しており、出典リンク付きで
参照する本用途はこれに当たる。`ai-train=no` も守る（学習には使わない）。
"""

from __future__ import annotations

import html
import json
import re
import sys
import time
import urllib.error
import urllib.request
from collections import Counter
from pathlib import Path

SITE = "https://wasa-birdman.com"
# 固定ページと投稿は性質が違う（前者は団体紹介などの構造的な情報、後者は活動報告）。
# 目次に載せる粒度を変えたいので、どちらの sitemap 由来かを記録する
SITEMAPS = {"page-sitemap.xml": "固定ページ", "post-sitemap.xml": "投稿"}
OUT = Path("dump/site.jsonl")
DELAY = 1.0  # 秒。縮めないこと
# HTTPヘッダは latin-1 しか通らないのでASCIIで書く（日本語だと送信時に落ちる）
UA = "WASAChat/0.1 (internal handover chatbot for WASA; cites with source links)"

# 本文の終わりを示す目印。ここから先はテーマの付属物なので捨てる
TAIL_MARKERS = (
    "Related Posts",
    "関連記事",
    "コメントを残す",
    "コメントを書く",
    "Tweets by",
    "前の投稿",
    "次の投稿",
)
# 本文が始まる前に出る定型
HEAD_NOISE = ("内容をスキップ", "コンテンツへスキップ", "メニュー", "Read More ...")

MIN_CHARS = 80  # これ未満は画像だけのページ。索引に入れても答えに使えない


def fetch(url: str, retries: int = 3) -> str:
    req = urllib.request.Request(url, headers={"User-Agent": UA})
    for attempt in range(retries):
        try:
            with urllib.request.urlopen(req, timeout=30) as res:
                raw = res.read()
                charset = res.headers.get_content_charset() or "utf-8"
                return raw.decode(charset, "replace")
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
    raise RuntimeError(url)


def sitemap_entries(name: str) -> list[tuple[str, str]]:
    """sitemap から (URL, 最終更新日) を取る。<loc> は CDATA で包まれている。"""
    xml = fetch(f"{SITE}/{name}")
    entries = []
    for block in re.findall(r"(?s)<url>(.*?)</url>", xml):
        loc = re.search(r"(?s)<loc>\s*(?:<!\[CDATA\[)?(.*?)(?:\]\]>)?\s*</loc>", block)
        mod = re.search(r"(?s)<lastmod>\s*(?:<!\[CDATA\[)?(.*?)(?:\]\]>)?\s*</lastmod>", block)
        if loc:
            entries.append((loc.group(1).strip(), (mod.group(1)[:10] if mod else "")))
    return entries


def page_title(doc: str) -> str:
    m = re.search(r"(?is)<title>(.*?)</title>", doc)
    if not m:
        return ""
    t = html.unescape(re.sub(r"\s+", " ", m.group(1))).strip()
    # 「記事名 - WASA鳥人間Project」からサイト名を落とす
    return re.sub(r"\s*[-|｜]\s*WASA鳥人間Project\s*$", "", t).strip()


def extract_text(doc: str) -> str:
    """HTMLから本文を取り出す。見出しは wikitext と同じ「== 見出し ==」で残す。

    このサイトは Elementor で作られており、WordPress標準の entry-content が無い。
    そのため data-elementor-type="wp-page" のブロックを本文とみなす。
    投稿ページにはこの属性が無いので、その場合はページ全体から
    ヘッダ・フッタ・ナビを削ったものを使う。
    """
    doc = re.sub(r"(?is)<!--.*?-->", " ", doc)
    doc = re.sub(r"(?is)<(script|style|noscript|svg|form|iframe)[^>]*>.*?</\1>", " ", doc)
    doc = re.sub(r"(?is)<(header|footer|nav|aside)\b[^>]*>.*?</\1>", " ", doc)

    body = re.search(r'(?is)<div[^>]+data-elementor-type="wp-page"[^>]*>(.*)', doc)
    if body:
        doc = body.group(1)

    # 見出しは wikitext と同じ「== 見出し ==」で残す。
    # build_index.py の split_sections がこの形を節の区切りとして読むため、
    # Wiki本文と同じ経路でチャンク化・パンくず付与ができる。
    #
    # 見出しの中身は必ず1行に畳む。<h2>2019<br>Canopus</h2> のような改行入りが
    # 実在し（機体一覧ページ）、そのままだと見出しの正規表現に当たらず
    # ただの本文になってしまう
    def heading(m: re.Match) -> str:
        inner = re.sub(r"(?s)<[^>]+>", " ", m.group("text"))
        inner = html.unescape(inner)
        inner = re.sub(r"[\s\xa0]+", " ", inner).strip()
        return f"\n\n== {inner} ==\n" if inner else "\n"

    doc = re.sub(r"(?is)<h([1-4])[^>]*>(?P<text>.*?)</h\1>", heading, doc)
    doc = re.sub(r"(?is)<li[^>]*>", "\n- ", doc)
    doc = re.sub(r"(?is)<br\s*/?>", "\n", doc)
    doc = re.sub(r"(?is)</(p|div|li|tr|td|h[1-6])>", "\n", doc)
    doc = re.sub(r"(?s)<[^>]+>", "", doc)
    doc = html.unescape(doc)

    lines: list[str] = []
    for line in doc.split("\n"):
        line = re.sub(r"[ \t　\xa0]+", " ", line).strip()
        if not line or line in HEAD_NOISE:
            continue
        # 記事タイトルはテーマの都合で2〜3回繰り返される（パンくず・見出し・本文頭）。
        # 同じ行が続いたら1回にまとめる
        if lines and lines[-1] == line:
            continue
        lines.append(line)
    text = "\n".join(lines)

    # テーマの付属物（関連記事・コメント欄・Twitter埋め込み）を落とす
    for marker in TAIL_MARKERS:
        idx = text.find(marker)
        if idx > 0:
            text = text[:idx]
    return text.strip()


def strip_boilerplate(records: list[dict]) -> None:
    """多くのページに共通して現れる行を落とす。

    サイドバーの「最新記事」一覧が全ページ末尾に混入する。ページごとに
    固定文言を書き並べるより、**出現頻度で判定するほうが壊れない**
    （サイトを更新しても追従する）。3割以上のページに出る短い行を定型とみなす。
    """
    if len(records) < 10:
        return
    counts = Counter()
    for r in records:
        counts.update({line for line in r["text"].split("\n") if len(line) <= 60})
    threshold = max(3, int(len(records) * 0.3))
    common = {line for line, n in counts.items() if n >= threshold and not line.startswith("==")}
    if not common:
        return
    for r in records:
        kept = [l for l in r["text"].split("\n") if l not in common]
        r["text"] = "\n".join(kept).strip()
    print(f"定型行を{len(common)}種類除去（{threshold}ページ以上に出現）")


def drop_title_line(records: list[dict]) -> None:
    """冒頭に出る「記事名 - WASA鳥人間Project」を落とす。

    **strip_boilerplate の後に呼ぶこと。** 定型行を除く前は別の行が先頭に来ており、
    先に判定すると一致せず素通りする（実際に499/510ページで残った）。
    """
    dropped = 0
    for r in records:
        lines = r["text"].split("\n")
        while lines and lines[0] in (f"{r['title']} - WASA鳥人間Project", r["title"],
                                     f"== {r['title']} =="):
            lines.pop(0)
            dropped += 1
        r["text"] = "\n".join(lines).strip()
    if dropped:
        print(f"冒頭の記事名の重複を{dropped}行除去")


def main() -> None:
    OUT.parent.mkdir(exist_ok=True)

    seen: dict[str, tuple[str, str]] = {}
    for name, kind in SITEMAPS.items():
        for url, mod in sitemap_entries(name):
            seen.setdefault(url, (mod, kind))  # 同じURLが複数のsitemapに出ることがある
        time.sleep(DELAY)
    print(f"対象 {len(seen)} ページ（"
          + " / ".join(f"{k} {sum(1 for v in seen.values() if v[1] == k)}"
                       for k in SITEMAPS.values()) + "）")

    records = []
    for i, (url, (mod, kind)) in enumerate(seen.items(), 1):
        try:
            doc = fetch(url)
        except Exception as e:  # noqa: BLE001 — 1ページ落ちても全体は続ける
            print(f"  取得失敗 {url}: {e}", file=sys.stderr)
            time.sleep(DELAY)
            continue
        title = page_title(doc) or url
        text = extract_text(doc)
        records.append({"url": url, "title": title, "kind": kind,
                        "text": text, "last_edited": mod})
        if i % 25 == 0 or i == len(seen):
            print(f"  {i}/{len(seen)}")
        time.sleep(DELAY)

    strip_boilerplate(records)
    drop_title_line(records)

    kept = [r for r in records if len(r["text"]) >= MIN_CHARS]
    dropped = len(records) - len(kept)

    with OUT.open("w", encoding="utf-8") as f:
        for r in kept:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")

    total = sum(len(r["text"]) for r in kept)
    print(f"\n{OUT} に {len(kept)} ページ / 計 {total:,} 字を書き出した"
          f"（本文{MIN_CHARS}字未満の{dropped}ページは除外）")
    for kind in SITEMAPS.values():
        group = [r for r in kept if r["kind"] == kind]
        if group:
            chars = sum(len(r["text"]) for r in group)
            print(f"  {kind}: {len(group)}ページ / {chars:,}字 / 平均{chars // len(group)}字")


if __name__ == "__main__":
    main()
