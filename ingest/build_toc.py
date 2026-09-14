"""data/index.json から、常時コンテキストに載せる目次を生成する。

設計方針 docs/01-設計方針.md §2 の L1（目次層）にあたる。
114ページという規模だからこそ、Wiki全体の見取り図をコンテキストに置ける。
Claudeは常に「どこに何があるか」を把握した状態で、必要なページだけを取りに行く。

情報密度で効いているのは **見出し一覧** である。「駆動・フレーム班」という
タイトルだけでは中身が分からないが、「40代引き継ぎ / ギア比 / チェーン」まで
見えれば、開くべきページかどうかを判断できる。要約LLMを呼ばなくても、
Wikiが持っている構造をそのまま使えばよい。

  python build_toc.py
出力: data/toc.md（Wiki本文を含むため .gitignore 対象）
"""

from __future__ import annotations

import json
from pathlib import Path

INDEX = Path("data/index.json")
OUT = Path("data/toc.md")

# 人が保守する事実カード。目次の**先頭**に埋め込む。
#
# 資料を素直に読むと間違える事柄（WASA全体の60周年を鳥人間プロジェクトの年数と
# 取り違える、代と西暦の対応が分からない）を確定事実として先に置く。
# 別ファイルとして Go 側に読ませる案もあったが、目次は「先頭に固定するとキャッシュが
# 効く」という条件で設計されている（backend/internal/llm/llm.go の Request 参照）。
# 目次そのものの先頭に入れてしまえば、キャッシュの条件も配置も一切変えずに済む。
FACTS = Path("facts.md")

LEAD_CHARS = 90  # リード文の長さ。目次全体のサイズはほぼここで決まる
MAX_HEADINGS = 10  # 1ページあたりに載せる見出しの数

# 公式サイトの活動報告（投稿）は479件あり、公式サイトの本文の94%を占める。
# ただし1件あたりは1,000字程度の作業記録で、Wikiのページほどの情報量はない。
#
# 載せ方を実測で比べた（v0=Wikiのみ 15,030字を1.00とする）:
#   A 固定ページのみ                    19,442字 (1.29倍)
#   B A + 投稿を年別・タイトルのみ        27,235字 (1.81倍)  ← 採用
#   C A + 投稿を1行・リード付き           53,278字 (3.54倍)
#
# Cは費用が3.5倍になるうえ、ローカル測定用の32kコンテキストに収まらず
# 測れなくなる。かといって投稿を落とすと、公式サイトを取り込む意味がほぼ消える。
# タイトル（「第七回試験飛行報告」など）だけで選べる程度に自己説明的なので、
# Bを既定とする。M2aで検索精度への影響を測ったうえでの判断（docs/08 §M6）。
INCLUDE_POSTS = True

# 班の並び順。技術班を先、運営系を後ろに置く
TEAM_ORDER = [
    "空力", "構造", "翼", "駆動・フレーム", "プロペラ", "フェアリング",
    "電装", "パイロット", "TF・大会", "運営", "代まとめ・人物", None,
]


def clip(text: str, limit: int) -> str:
    return text if len(text) <= limit else text[:limit].rstrip() + "…"


def render_posts(posts: list[dict]) -> list[str]:
    """活動報告は年ごとにまとめ、タイトルだけを並べる。

    1件ずつリード文を付けると目次が3.5倍に膨らむ。タイトルが
    「第七回試験飛行報告」「全組試験および回転試験実施報告」のように
    自己説明的なので、選ぶだけならタイトルで足りる。
    """
    by_year: dict[str, list[str]] = {}
    for page in posts:
        by_year.setdefault(page["last_edited"][:4] or "年不明", []).append(page["title"])

    lines = ["各記事は1,000字程度の作業・試験の記録。タイトルをそのまま指定して開く。", ""]
    for year in sorted(by_year, reverse=True):
        titles = sorted(by_year[year])
        lines.append(f"- **{year}年**（{len(titles)}件）: " + " / ".join(titles))
    return lines


def render_page(page: dict) -> list[str]:
    tags = []
    if page["gen"]:
        # 本文由来の代は推定でしかないので「?」で区別する（タイトル由来は確定）
        tags.append(f"{page['gen']}代" + ("?" if page["gen_source"] == "body" else ""))
    elif len(page.get("gens_mentioned") or []) >= 2:
        # 代を跨いで書き足していくページ。単一の代を貼るとどれかで必ず外すので、
        # 扱っている範囲をそのまま出す。「歴代でどう変遷したか」を聞かれたときに
        # 開くべきページが目次から分かる
        gens = page["gens_mentioned"]
        tags.append(f"{gens[0]}〜{gens[-1]}代")
    tags.append(page["last_edited"][:7])
    if page["is_stub"]:
        tags.append("中身なし")
    else:
        tags.append(f"{page['chars'] // 1000}k字" if page["chars"] >= 1000 else f"{page['chars']}字")

    title = page["title"]
    if page["aliases"]:
        title += "（別名: " + "、".join(page["aliases"]) + "）"

    lines = [f"- **{title}** [{' | '.join(tags)}] {clip(page['lead'], LEAD_CHARS)}"]
    if page["headings"]:
        shown = page["headings"][:MAX_HEADINGS]
        more = len(page["headings"]) - len(shown)
        lines.append("  節: " + " / ".join(shown) + (f" ほか{more}件" if more else ""))
    return lines


def load_facts() -> list[str]:
    """事実カードの本文を読む。`---` より前は保守用の注意書きなので落とす。

    プロンプトに載る分量を人が目視で管理できるようにするための約束事。
    ファイルが無くても目次は生成できる（事実カードは後から足した層である）。
    """
    if not FACTS.exists():
        return []
    text = FACTS.read_text(encoding="utf-8")
    _, sep, body = text.partition("\n---\n")
    return (body if sep else text).strip().split("\n") + [""]


def main() -> None:
    all_pages = json.loads(INDEX.read_text(encoding="utf-8"))["pages"]
    pages = [p for p in all_pages if p.get("source", "wiki") == "wiki"]
    site = [p for p in all_pages if p.get("source") == "site"]
    fee = [p for p in all_pages if p.get("source") == "fee"]
    drive = [p for p in all_pages if p.get("source") == "drive"]

    order = {team: i for i, team in enumerate(TEAM_ORDER)}
    pages.sort(
        key=lambda p: (
            order.get(p["team"], len(TEAM_ORDER)),
            # 新しい代を上に。単一の代を決められないページは、扱っている
            # 最も新しい代で並べる。0にすると、代を跨ぐ主要な引き継ぎページが
            # 班の一番下へ沈んでしまう
            -(p["gen"] or (p.get("gens_mentioned") or [0])[-1]),
            p["title"],
        )
    )

    facts = load_facts()
    lines = facts + [
        "# WASA 資料の目次",
        "",
        "出所が3つある。**引き継ぎWiki**（部内限定・作業手順や設計の詳細）、",
        "**公式サイト**（一般公開・団体紹介や活動報告）、",
        "**フライトシミュレータ**（FlightEnvironmentEmulatorの利用ガイド）。",
        "対外的な説明や歴代機体は公式サイト、作り方や反省点はWiki、",
        "シミュレータの導入・操作・設定はフライトシミュレータのガイドにある。",
        "",
        f"## 引き継ぎWiki（部内限定）全{len(pages)}ページ",
        "",
        "「中身なし」はページは存在するが本文がほぼ無いもの。",
        "代の「?」は本文からの推定で、そのページ自体の代とは限らない。",
        "",
    ]
    current_team = object()
    for page in pages:
        if page["team"] != current_team:
            current_team = page["team"]
            lines += ["", f"### {current_team or 'その他'}", ""]
        lines += render_page(page)

    if site:
        fixed = [p for p in site if p.get("kind") == "固定ページ"]
        posts = [p for p in site if p.get("kind") != "固定ページ"]
        shown = len(site) if INCLUDE_POSTS else len(fixed)
        lines += ["", f"## 公式サイト（一般公開 wasa-birdman.com）全{shown}ページ", ""]
        if fixed:
            lines += ["### 団体紹介・機体一覧など", ""]
            for page in sorted(fixed, key=lambda p: p["title"]):
                lines += render_page(page)
        if posts and INCLUDE_POSTS:
            lines += ["", "### 活動報告（ブログ・新しい年から）", ""]
            lines += render_posts(posts)

    if fee:
        lines += ["", f"## フライトシミュレータのガイド（FlightEnvironmentEmulator）全{len(fee)}ページ", ""]
        lines += ["機体の資料ではなく、飛行シミュレータというソフトの使い方・設定・開発の資料。", ""]
        for page in sorted(fee, key=lambda p: (p.get("kind") != "紹介", p["title"])):
            lines += render_page(page)

    # ⚠️ **共有ドライブは必ず最後に置く。** ここより前は「常に載せる固定
    # プレフィックス」で、プロンプトキャッシュが効く部分である。Driveを間に
    # 混ぜると、Driveのファイルが1つ増減するたびに前半のバイト列まで変わり、
    # **キャッシュが毎回外れる**。また、Driveをオフにしている会話へファイル名が
    # 漏れることにもなる（2026-09-13のCodex指摘）。
    #
    # サーバー側（index.Build）はこの見出しで切り、Driveがオンのときだけ後ろを足す。
    if drive:
        lines += ["", f"## 共有ドライブ（部内限定）全{len(drive)}ファイル", ""]
        lines += ["Wikiに書かれていない議事録・設計メモ・報告書。"
                  "入力欄の「+」で参照先に足したときだけ読む。", ""]
        for page in sorted(drive, key=lambda p: (p.get("kind", ""), p["title"])):
            lines += render_page(page)

    toc = "\n".join(lines) + "\n"
    OUT.parent.mkdir(exist_ok=True)
    OUT.write_text(toc, encoding="utf-8")

    # ---- サイズ報告 ----
    # 日本語はおおむね 1〜1.5 字/トークン。正確な値は API の count_tokens で測る
    chars = len(toc)
    print("=" * 56)
    print(f"目次を生成      : {OUT}")
    print(f"ページ数        : {len(pages) + len(site) + len(fee) + len(drive)}"
          f"（Wiki {len(pages)} / 公式サイト {len(site)} / フライトシミュレータ {len(fee)}"
          f" / 共有ドライブ {len(drive)}）")
    print(f"文字数          : {chars:,} 字（うち事実カード {len(chr(10).join(facts)):,} 字）")
    print(f"推定トークン数  : {int(chars / 1.5):,} 〜 {chars:,}")
    print(f"1ページあたり   : {chars // max(1, len(pages) + len(site) + len(fee) + len(drive))} 字")
    if drive:
        # 固定プレフィックスに入るのは共有ドライブ節より前だけ。ここを膨らませない
        drive_at = toc.index("## 共有ドライブ（部内限定）")
        print(f"  うち固定部分  : {drive_at:,} 字（共有ドライブ {chars - drive_at:,} 字は"
              f"「+」でオンにしたときだけ載る）")

    # プロンプトキャッシュに載る前提での概算（Sonnet 5 / 1時間TTL、$1=150円）
    tokens = chars  # 上振れ側で見積もる
    print(f"\nキャッシュ書込  : 約 ¥{tokens / 1_000_000 * 2 * 2 * 150:.0f}（1時間ごと）")
    print(f"キャッシュ読出  : 約 ¥{tokens / 1_000_000 * 0.2 * 150:.1f} / 質問")
    print("=" * 56)


if __name__ == "__main__":
    main()
