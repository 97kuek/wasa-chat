/**
 * markdown.tsx の回帰テスト。
 *
 *   npm test
 *
 * 新しい依存は入れていない。Node標準のテストランナーと、Viteが持っている
 * esbuild だけで動かす。TypeScript/JSX と CSS の import があるので、
 * 一度バンドルしてから読み込む。
 *
 * ここを守りたい一番の理由は、**描画が止まらなくなるとタブごと落ちる**ことである。
 * 例外なら境界が受け止められるが、無限ループは受け止められない。
 */

import assert from "node:assert/strict";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { pathToFileURL } from "node:url";

import { build } from "esbuild";

const outfile = join(mkdtempSync(join(tmpdir(), "wasa-md-")), "markdown.mjs");
await build({
  entryPoints: ["src/markdown.tsx"],
  bundle: true,
  format: "esm",
  platform: "node",
  outfile,
  loader: { ".css": "empty" },
});
const { renderMarkdown } = await import(pathToFileURL(outfile).href);

/** 無限ループを「時間切れ」ではなく確実に捕まえる。 */
function renderWithin(text, maxOperations = 100_000) {
  const originalPush = Array.prototype.push;
  let operations = 0;
  Array.prototype.push = function (...items) {
    if (++operations > maxOperations) {
      Array.prototype.push = originalPush;
      throw new Error("描画が終わりません（無限ループの可能性）");
    }
    return originalPush.apply(this, items);
  };
  try {
    return renderMarkdown(text);
  } finally {
    Array.prototype.push = originalPush;
  }
}

// 回答はSSEで少しずつ届く。**途中のどの状態でも描画が終わらなければならない。**
// 表の1行目だけが届いた状態で無限ループし、タブが固まって落ちていた（2026-08-10）。
test("届きかけの本文でも、どの長さで切っても描画が終わる", () => {
  const answer = [
    "## 40代の空力設計",
    "",
    "主翼の翼型には **DAE31** を採用しています。",
    "",
    "| 項目 | 40代 | 41代 |",
    "|---|---|---|",
    "| 翼型 | DAE31 | DAE31改 |",
    "",
    "揚抗比は \\( C_L / C_D \\) で表され、$L/D = 38$ を狙います。",
    "",
    "> 引用も混ぜておく",
    "",
    "1. 手順その1",
    "2. 手順その2",
    "",
    "- [空力設計(41st)](https://example.org/a)（Wiki、本文の年代: 2025年）",
  ].join("\n");

  for (let length = 1; length <= answer.length; length++) {
    const partial = answer.slice(0, length);
    assert.doesNotThrow(
      () => renderWithin(partial),
      `${length}字目で止まらなくなりました: ${JSON.stringify(partial.slice(-30))}`,
    );
  }
});

test("区切り行の無い表の行は、段落として1行ずつ進む", () => {
  const html = renderWithin("| 項目 | 40代 |");
  assert.match(html, /<p>/);
  assert.doesNotMatch(html, /<table>/);
});

test("区切り行が揃っていれば表として描く", () => {
  const html = renderWithin("| 項目 | 40代 |\n|---|---|\n| 翼型 | DAE31 |");
  assert.match(html, /<table>/);
  assert.match(html, /<th>項目<\/th>/);
  assert.match(html, /<td>DAE31<\/td>/);
});

// 生成文にはWiki本文が入る。人が書いたHTMLが混ざっても通してはいけない。
test("HTMLは常にエスケープする", () => {
  const html = renderWithin('<img src=x onerror="alert(1)">');
  assert.doesNotMatch(html, /<img/);
  assert.match(html, /&lt;img/);
});

test("javascript: のリンクは素通しせず、文字として残す", () => {
  const html = renderWithin("[押して](javascript:alert(1))");
  assert.doesNotMatch(html, /href="javascript/);
  assert.match(html, /押して/);
});

test("見出し・箇条書き・強調・コードを描く", () => {
  assert.match(renderWithin("# 見出し"), /<h3>見出し<\/h3>/);
  assert.match(renderWithin("- ひとつ\n- ふたつ"), /<ul><li>ひとつ<\/li><li>ふたつ<\/li><\/ul>/);
  assert.match(renderWithin("**太字**"), /<strong>太字<\/strong>/);
  assert.match(renderWithin("`code`"), /<code>code<\/code>/);
});

// コード中の $ を数式にすると、金額や変数名が壊れる
test("コード中の $ は数式にしない", () => {
  const html = renderWithin("`$HOME` と `$PATH`");
  assert.match(html, /<code>\$HOME<\/code>/);
  assert.match(html, /<code>\$PATH<\/code>/);
});

test("閉じていない数式ブロックは原文のまま段落へ戻す", () => {
  assert.doesNotThrow(() => renderWithin("$$\n\\frac{1}{2}\n"));
});

// ここから下は 2026-08-11 に見つけた3件。どれも「実際にこう描かれていた」を固定する。

// コマンドや型番がそのまま化ける。中身をMarkdownとして解釈してはいけない。
test("フェンス付きコードブロックの中身は記法として解釈しない", () => {
  const html = renderWithin("```bash\n# コメント\n**強調しない**\n| a | b |\n```");
  assert.match(html, /<pre class="code-block"><code>/);
  assert.match(html, /# コメント/);
  assert.doesNotMatch(html, /<h3>/);
  assert.doesNotMatch(html, /<strong>/);
  assert.doesNotMatch(html, /<table>/);
});

// 開始の ``` だけが届いた状態は、ストリーミングでは必ず通る。
test("閉じていないコードブロックも、そこまでをコードとして描く", () => {
  const html = renderWithin("```bash\npython rebuild.py");
  assert.match(html, /<pre class="code-block"><code>python rebuild\.py<\/code><\/pre>/);
});

test("コードブロック内のHTMLもエスケープする", () => {
  const html = renderWithin('```\n<img src=x onerror="alert(1)">\n```');
  assert.doesNotMatch(html, /<img/);
  assert.match(html, /&lt;img/);
});

// 平坦に読むと手順の親子関係が消える。
test("字下げされた箇条書きは入れ子にする", () => {
  assert.match(
    renderWithin("- 親1\n  - 子1\n  - 子2\n- 親2"),
    /<ul><li>親1<ul><li>子1<\/li><li>子2<\/li><\/ul><\/li><li>親2<\/li><\/ul>/,
  );
});

test("番号付きの中の箇条書きも入れ子にする", () => {
  const html = renderWithin("1. 親\n   - 子A\n2. 次");
  assert.match(html, /<ol><li>親<ul><li>子A<\/li><\/ul><\/li><li>次<\/li><\/ol>/);
});

// 以前はリストが2つに割れ、継続行が先頭の空白ごと段落になっていた。
test("字下げした継続行は同じ項目の中に入れる", () => {
  const html = renderWithin("- 手順1\n  補足の説明\n- 手順2");
  assert.match(html, /<ul><li>手順1<br>補足の説明<\/li><li>手順2<\/li><\/ul>/);
});

test("項目の間に空行があっても1つのリストにする", () => {
  const html = renderWithin("- 項目A\n\n- 項目B");
  assert.match(html, /<ul><li>項目A<\/li><li>項目B<\/li><\/ul>/);
  assert.equal(html.match(/<ul>/g).length, 1);
});

test("リストの次の段落はリストの外に出す", () => {
  assert.match(renderWithin("- a\n次の段落"), /<ul><li>a<\/li><\/ul><p>次の段落<\/p>/);
});

// ---- 本文中の出典チップ（2026-08-11に追加） ----------------------------
//
// モデルが書けるのは**番号だけ**である。題名とURLはサーバーが索引から
// 組み立てたものを渡す。番号の解決を間違えると、別の資料が根拠として出る。

const CITES = [
  { title: "リファラルご飯制度", url: "https://example.org/a" },
  { title: "経費申請方法", url: "https://example.org/b" },
];

test("[1] は1件目の出典チップになる", () => {
  const html = renderMarkdown("税込2,500円まで出ます。[1]", CITES);
  assert.match(html, /<a class="citation" href="https:\/\/example\.org\/a"/);
  assert.match(html, /title="リファラルご飯制度"/);
  assert.doesNotMatch(html, /\[1\]/);
});

test("[1][2] は2つのチップになる", () => {
  const html = renderMarkdown("領収書をもらいます。[1][2]", CITES);
  assert.equal(html.match(/class="citation"/g).length, 2);
});

test("句点の前に来た出典番号は、句点の後へ移してからチップにする", () => {
  const html = renderMarkdown("主翼を確認します[1]。", CITES);
  assert.match(html, /確認します。<a class="citation"/);
  assert.doesNotMatch(html, /<\/a>。/);
});

// モデルが番号を作ったとき、意味のない [9] を本文に残さない
test("範囲外の番号は消す", () => {
  const html = renderMarkdown("そんな資料はありません。[9]", CITES);
  assert.doesNotMatch(html, /\[9\]/);
  assert.doesNotMatch(html, /class="citation"/);
});

// 出典が無い場面（履歴の読み直しなど）で本文を勝手に削らない
test("出典が空なら本文の [1] に触らない", () => {
  assert.match(renderMarkdown("配列の [1] 番目", []), /\[1\]/);
});

test("コード中の [1] はチップにしない", () => {
  const html = renderMarkdown("`array[1]` と書きます。[2]", CITES);
  assert.match(html, /<code>array\[1\]<\/code>/);
  assert.equal(html.match(/class="citation"/g).length, 1);
});

test("チップの題名と属性もエスケープする", () => {
  const html = renderMarkdown("危ない題名。[1]", [
    { title: '<img src=x onerror="alert(1)">', url: "javascript:alert(1)" },
  ]);
  assert.doesNotMatch(html, /<img/);
  assert.doesNotMatch(html, /href="javascript/);
  assert.match(html, /class="citation"/);
});

// 入れ子と継続行を足したので、止まらなくなる経路が増えていないかを見る。
test("コードと入れ子リストを含む本文でも、どの長さで切っても描画が終わる", () => {
  const answer = [
    "## 索引の作り直し",
    "",
    "```bash",
    "python check_updates.py",
    "python rebuild.py",
    "```",
    "",
    "- 事前に確認すること",
    "  - `.env` がコミットに出ていない",
    "  - ページ数が100件を下回っていない",
    "    1. 取得が途中で失敗していないか",
    "    2. ログに429が出ていないか",
    "",
    "- 反映",
    "  差し替えたあと、環境変数を1つ動かす",
    "",
    "| 手順 | 時間 |",
    "|---|---|",
    "| 資料だけ | 1分 |",
  ].join("\n");

  for (let length = 1; length <= answer.length; length++) {
    const partial = answer.slice(0, length);
    assert.doesNotThrow(
      () => renderWithin(partial),
      `${length}字目で止まらなくなりました: ${JSON.stringify(partial.slice(-30))}`,
    );
  }
});

// ---------------------------------------------------------------- 図と表

test("表の区切り行の `:` を寄せとして反映する", () => {
  const html = renderWithin("| 項目 | 数量 | 単価 |\n|---|---:|:-:|\n| りんご | 3 | 150円 |");
  assert.match(html, /<th>項目<\/th>/);
  assert.match(html, /<th class="col-right">数量<\/th>/);
  assert.match(html, /<th class="col-center">単価<\/th>/);
  assert.match(html, /<td class="col-right">3<\/td>/);
});

test("表にはCSV保存のボタンを添える", () => {
  const html = renderWithin("| 項目 | 数量 |\n|---|---|\n| りんご | 3 |");
  assert.match(html, /<figure class="data-table">/);
  assert.match(html, /data-figure-action="download-csv"/);
});

// 番号付きの手順が説明を挟んで途切れたとき、画面が「1.」へ戻すと手順の指示が壊れる。
test("番号付きは書かれている番号から始める", () => {
  assert.match(renderWithin("4. 桁を組む\n5. リブを通す"), /<ol start="4">/);
  assert.match(renderWithin("1. 桁を組む"), /<ol>/);
});

// **閉じるまで図にしない。** 途中まで届いたMermaidは必ず構文が壊れており、
// 描こうとすると1デルタごとに解析エラーになる。
test("閉じたmermaidだけを図の入れ物にする", () => {
  const closed = renderWithin("```mermaid\nflowchart LR\n  A --> B\n```");
  assert.match(closed, /<figure class="diagram" data-diagram="/);
  assert.match(closed, /data-figure-action="zoom-in"/);

  const open = renderWithin("```mermaid\nflowchart LR\n  A --> B");
  assert.doesNotMatch(open, /<figure/);
  assert.match(open, /<pre class="code-block">/);
});

// 図が描けなくても原文は読めなければならない。描画は後から差し替える方式なので、
// 入れ物の中には必ずコードブロックが入っている。
test("図の入れ物には原文のコードも残す", () => {
  const html = renderWithin("```mermaid\nflowchart LR\n  A --> B\n```");
  assert.match(html, /<pre class="code-block diagram-code"><code>flowchart LR/);
});

// Mermaidの原文は data 属性へ入る。ここがエスケープ漏れすると、
// 「先に全部エスケープする」という markdown.tsx の前提がそこだけ崩れる。
test("図の原文も属性としてエスケープする", () => {
  const html = renderWithin('```mermaid\nflowchart LR\n  A["\\"><img src=x onerror=alert(1)>"]\n```');
  assert.doesNotMatch(html, /<img/);
  assert.match(html, /&quot;|&gt;/);
});

test("mermaid以外の言語指定はこれまでどおりコードブロックにする", () => {
  const html = renderWithin("```bash\npython rebuild.py\n```");
  assert.match(html, /<pre class="code-block"><code>python rebuild\.py/);
  assert.doesNotMatch(html, /<figure/);
});

// 本番で、モデルが図の中身は正しく書きながら言語名を付けずにコードブロックへ
// 入れてきた（2026-09-12）。言語名だけで判定すると図にならない。
test("言語名が無くても、先頭行が図の宣言なら図にする", () => {
  for (const head of ["flowchart TD", "graph LR", "sequenceDiagram", "stateDiagram-v2"]) {
    const html = renderWithin("```\n" + head + "\n  A --> B\n```");
    assert.match(html, /<figure class="diagram"/, `${head} が図にならない`);
  }
});

test("先頭行が図の宣言でなければコードブロックのままにする", () => {
  for (const body of ["python rebuild.py", "graph = build()", "flowchart は図の記法です"]) {
    const html = renderWithin("```\n" + body + "\n```");
    assert.doesNotMatch(html, /<figure/, `${body} を図にしてはいけない`);
  }
});

// 言語名が明示されているものは尊重する。bashのコマンドを図にしない
test("別の言語名が付いていれば図にしない", () => {
  const html = renderWithin("```bash\ngraph LR\n```");
  assert.doesNotMatch(html, /<figure/);
});
