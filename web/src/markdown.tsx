import { memo, useEffect, useRef, type MouseEvent } from "react";
import katex from "katex";
import "katex/dist/katex.min.css";

import { copyText, downloadTableCSV, downloadDiagram, renderDiagrams, zoomDiagram } from "./diagram";

/**
 * 回答本文のMarkdownを描画する。
 *
 * 外部ライブラリを使わず自前で持っている理由は安全性である。
 * 生成文にはWiki本文（＝人が書いたHTMLが混ざりうるテキスト）が入る。
 * ライブラリ＋サニタイザの構成は「危険なものを後から除く」形になるが、
 * ここでは **先にすべてエスケープしてから整形する** ので、
 * 生のHTMLが通る経路が構造的に存在しない。
 *
 * ⚠️ **例外は Mermaid の図だけである**（2026-09-12に追加）。Mermaidは
 * テキストからSVGを組み立てて差し込むので、この「先に全部エスケープする」
 * 原則の外にある唯一の経路になる。そのため図の描画は `diagram.ts` へ隔離し、
 * ここでは**図の原文をエスケープして data 属性へ置くだけ**にしてある。
 * 描画に失敗したときはコードブロックのまま残す（原文は必ず読める）。
 *
 * 対応する記法は生成文に実際に出るものだけ:
 * 見出し / 箇条書き（入れ子・継続行つき） / 番号付き / 表 / 引用 / 水平線 /
 * 強調 / コード（行内・フェンス付き） / リンク / TeX数式 / Mermaidの図。
 */

type Inline = { html: string };

const escapeHtml = (s: string) =>
  s.replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!,
  );

/** リンク先は http/https だけ許可する（javascript: などを弾く） */
const safeHref = (url: string) => (/^https?:\/\//i.test(url) ? url : null);

/**
 * 本文に差し込む出典。`[1]` の番号を、この配列の1件目へ対応させる。
 *
 * **モデルが書けるのは番号だけである。** 表示するタイトルとURLは、
 * サーバーが索引から組み立てたものをここへ渡す。こうしないと
 * 「引き継ぎWiki」のような実在しない出典名を本文へ書けてしまう（docs/02）。
 */
export type Citation = { title: string; url: string };

/** 描画中の出典。renderMarkdown の入口でだけ差し替える。 */
let citations: Citation[] = [];

/** チップに出す題名の長さ。長い題名で本文が読めなくなるのを防ぐ。 */
const CITATION_LABEL_RUNES = 14;

const shortTitle = (title: string) => {
  const runes = Array.from(title);
  return runes.length <= CITATION_LABEL_RUNES
    ? title
    : runes.slice(0, CITATION_LABEL_RUNES).join("") + "…";
};

/**
 * `[1]` や `[1][3]` を出典チップへ変える。
 *
 * **範囲外の番号は消す。** 文字として残すと、モデルが番号を作ったときに
 * 意味のない `[9]` が本文に残る。出典が1件も無いときは何もしない
 * （番号の付いた箇条書きを壊さないため）。
 */
function citationChips(html: string): string {
  if (citations.length === 0) return html;
  // 日本語の句点より前に番号を置く生成が多いが、チップが文中へ割り込んで見える。
  // コードと数式はすでに退避済みなので、本文の連続した出典番号だけを句点の後へ送る。
  const punctuationNormalized = html.replace(
    /((?:\[\d{1,2}\])+)([。．.!！？?])/g,
    "$2$1",
  );
  return punctuationNormalized.replace(/(?:\[(\d{1,2})\])+/g, (match) => {
    const chips = Array.from(match.matchAll(/\[(\d{1,2})\]/g))
      .map(([, digits]) => citations[Number(digits) - 1])
      .filter((citation): citation is Citation => Boolean(citation))
      .map((citation) => {
        const href = safeHref(citation.url);
        const label = escapeHtml(shortTitle(citation.title));
        const full = escapeHtml(citation.title);
        const body = `<span class="citation-mark" aria-hidden="true"></span>${label}`;
        return href
          ? `<a class="citation" href="${escapeHtml(href)}" target="_blank" rel="noreferrer noopener" title="${full}">${body}</a>`
          : `<span class="citation" title="${full}">${body}</span>`;
      });
    return chips.join("");
  });
}

function inline(raw: string): Inline {
  const tokens: string[] = [];
  const stash = (html: string) => {
    const marker = `\uE000${tokens.length}\uE001`;
    tokens.push(html);
    return marker;
  };

  // コード中の $ は数式にしない。先にコードを退避してからTeXを探す。
  let protectedText = raw.replace(/`([^`]+)`/g, (_, code: string) =>
    stash(`<code>${escapeHtml(code)}</code>`),
  );
  protectedText = protectedText.replace(
    /\\\(([\s\S]+?)\\\)|(?<!\\)\$([^$\n]+?)(?<!\\)\$/g,
    (_, paren: string | undefined, dollar: string | undefined) =>
      // 2つの選択肢のどちらか一方だけが一致するが、型の上では両方 undefined
      // になりうる。空文字に落として renderMath の引数型を満たす
      stash(renderMath(paren ?? dollar ?? "", false)),
  );

  let html = escapeHtml(protectedText);
  // **`<br>` だけは改行として通す。** モデルは表のセルの中で改行するのに
  // これを使う（Markdownの表はセル内改行を書けないため）。エスケープしたままだと
  // 「<br>」という文字がそのまま表に並ぶ（2026-09-12に本番で確認）。
  // 閉じタグも属性も取らない要素なので、ここを通しても書ける形は増えない
  html = html.replace(/&lt;br\s*\/?&gt;/gi, "<br>");
  html = html.replace(/\*\*\*(.+?)\*\*\*/g, "<strong><em>$1</em></strong>");
  html = html.replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>");
  html = html.replace(/(^|[^*])\*([^*\n]+)\*/g, "$1<em>$2</em>");
  html = html.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, (_, label: string, url: string) => {
    const href = safeHref(url);
    return href
      ? `<a href="${href}" target="_blank" rel="noreferrer noopener">${label}</a>`
      : label;
  });
  // 素のURLもリンクにする
  html = html.replace(
    /(^|[\s(])(https?:\/\/[^\s<)]+)/g,
    '$1<a href="$2" target="_blank" rel="noreferrer noopener">$2</a>',
  );
  // 出典チップは、退避したコードと数式を戻す**前**に作る。戻したあとだと、
  // コード中の `[1]` までチップになってしまう
  html = citationChips(html);
  html = html.replace(/\uE000(\d+)\uE001/g, (_, index: string) => tokens[Number(index)] ?? "");
  return { html };
}

/** KaTeXは既定で危険なHTML命令を許可しない。失敗時もTeX原文を安全に表示する。 */
function renderMath(source: string, displayMode: boolean): string {
  return katex.renderToString(source.trim(), {
    displayMode,
    throwOnError: false,
    strict: "warn",
    trust: false,
    output: "htmlAndMathml",
  });
}

/** $$ ... $$ と \[ ... \] の複数行ブロックを1つの数式として取り出す。 */
function blockMath(lines: string[], start: number): { html: string; next: number } | null {
  const trimmed = lines[start].trim();
  const delimiters = trimmed.startsWith("$$")
    ? { open: "$$", close: "$$" }
    : trimmed.startsWith("\\[")
      ? { open: "\\[", close: "\\]" }
      : null;
  if (!delimiters) return null;

  const first = trimmed.slice(delimiters.open.length);
  if (first.endsWith(delimiters.close)) {
    return {
      html: `<div class="math-block">${renderMath(first.slice(0, -delimiters.close.length), true)}</div>`,
      next: start + 1,
    };
  }

  const formula = [first];
  let i = start + 1;
  while (i < lines.length) {
    const end = lines[i].indexOf(delimiters.close);
    if (end !== -1) {
      formula.push(lines[i].slice(0, end));
      return {
        html: `<div class="math-block">${renderMath(formula.join("\n"), true)}</div>`,
        next: i + 1,
      };
    }
    formula.push(lines[i]);
    i++;
  }

  // 閉じ忘れは数式として解釈せず、原文を通常の段落へ戻す。
  return null;
}

const cells = (line: string) =>
  line
    .trim()
    .replace(/^\||\|$/g, "")
    .split("|")
    .map((c) => c.trim());

const isDivider = (line: string) => /^\|?[\s:|-]+\|[\s:|-]*$/.test(line.trim());

/**
 * 区切り行の `:` から列ごとの寄せを読む（`|---:|` は右寄せ、`|:--:|` は中央）。
 *
 * 以前は区切り行を「表かどうか」の判定にしか使っておらず、**寄せの指定を
 * 捨てていた。** 数量や金額の列は右寄せで出力されることが多く、
 * 左寄せのまま並ぶと桁が揃わずに読みにくい。
 */
const alignmentsOf = (divider: string) =>
  cells(divider).map((cell) => {
    const head = cell.startsWith(":");
    const tail = cell.endsWith(":");
    if (head && tail) return ' class="col-center"';
    if (tail) return ' class="col-right"';
    return "";
  });

/** ``` または ~~~ で囲まれたコードブロック。言語名は mermaid の判定にだけ使う。 */
const FENCE_OPEN = /^\s*(`{3,}|~{3,})\s*(\S*)\s*$/;
const FENCE_CLOSE = /^\s*(`{3,}|~{3,})\s*$/;

/**
 * 図と表に添えるボタンの絵柄。
 *
 * 画面のほかの場所と同じく、外部のアイコンフォントは足さずにSVGを直接書く。
 * `stroke="currentColor"` にしてあるので、色はCSS側だけで決まる。
 */
const ICONS = {
  copy: '<path d="M9 9V5.5A1.5 1.5 0 0 1 10.5 4h8A1.5 1.5 0 0 1 20 5.5v8a1.5 1.5 0 0 1-1.5 1.5H15"/><rect x="4" y="9" width="11" height="11" rx="1.5"/>',
  zoomOut: '<circle cx="11" cy="11" r="6"/><path d="M8.5 11h5M20 20l-4.7-4.7"/>',
  zoomIn: '<circle cx="11" cy="11" r="6"/><path d="M8.5 11h5M11 8.5v5M20 20l-4.7-4.7"/>',
  download: '<path d="M12 4v11m0 0 4-4m-4 4-4-4M4 19h16"/>',
  source: '<path d="m9 8-5 4 5 4M15 8l5 4-5 4"/>',
} as const;

const iconButton = (action: string, icon: string, label: string) =>
  `<button type="button" class="figure-button" data-figure-action="${action}" title="${label}" aria-label="${label}">` +
  `<svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.7" ` +
  `stroke-linecap="round" stroke-linejoin="round">${icon}</svg></button>`;

/**
 * フェンス付きコードブロックを取り出す。
 *
 * **閉じていなくても、そこまでをコードとして描く。** 回答はSSEで少しずつ届くので、
 * 開始の ``` だけが届いた状態を必ず通る。閉じるまで通常の行として扱っていると、
 * 中の `#` が見出しに、`**` が強調に化けたあと、閉じた瞬間に組み変わる。
 * コマンドや型番がそのまま化けるので、途中でもコードとして固定するほうが正しい
 * （2026-08-11に確認）。
 */
function fencedCode(lines: string[], start: number): { html: string; next: number } | null {
  const open = lines[start].match(FENCE_OPEN);
  if (!open) return null;
  const fence = open[1];
  const language = open[2].toLowerCase();
  const body: string[] = [];
  let i = start + 1;
  let closed = false;
  while (i < lines.length) {
    const close = lines[i].match(FENCE_CLOSE);
    // 開いたときと同じ記号で、同じ長さ以上のときだけ閉じる
    if (close && close[1][0] === fence[0] && close[1].length >= fence.length) {
      i++;
      closed = true;
      break;
    }
    body.push(lines[i]);
    i++;
  }
  const source = body.join("\n");
  const code = `<pre class="code-block"><code>${escapeHtml(source)}</code></pre>`;

  // **閉じるまでは図にしない。** 途中まで届いたMermaidは必ず構文として壊れており、
  // 描こうとすると1デルタごとに解析エラーが出る。閉じるまでコードとして見せ、
  // 閉じた瞬間に図へ差し替える（コードブロックの扱いと同じ考え方）
  if (closed && source.trim() && isMermaid(language, source)) {
    return { html: diagramFigure(source), next: i };
  }
  return { html: code, next: i };
}

/** Mermaidの図の宣言。先頭行がこれなら、言語名が無くても図として扱う。 */
const MERMAID_HEADER =
  /^(?:flowchart|graph)\s+(?:TB|TD|BT|RL|LR)\b|^(?:sequenceDiagram|classDiagram|stateDiagram(?:-v2)?|erDiagram|journey|gantt|pie|mindmap|timeline|quadrantChart|gitGraph)\b/;

/**
 * Mermaidかどうかを判定する。
 *
 * **言語名の指定を当てにしない。** 実際に、モデルが図の中身は正しく書きながら
 * 言語名を付けずにコードブロックへ入れてきた（2026-09-12に本番で確認）。
 * 言語名だけで判定すると、その場合は図にならずコードのまま出る。
 * 別の言語名が明示されているときは尊重する（bashのコマンドを図にしないため）。
 */
function isMermaid(language: string, source: string): boolean {
  if (language === "mermaid") return true;
  if (language !== "") return false;
  const first = source.split("\n").find((line) => line.trim())?.trim() ?? "";
  return MERMAID_HEADER.test(first);
}

/**
 * Mermaidの図の入れ物を返す。この時点ではまだ図になっておらず、原文のコードが入る。
 *
 * 実際の描画は `diagram.ts` が後から行い、成功したときだけ `is-rendered` が付く。
 * **描画できなくてもコードは残る**ので、図が出ないことで情報そのものが消えることはない。
 */
function diagramFigure(source: string): string {
  const toolbar =
    iconButton("copy", ICONS.copy, "図の記述をコピー") +
    iconButton("zoom-out", ICONS.zoomOut, "縮小") +
    iconButton("zoom-in", ICONS.zoomIn, "拡大") +
    iconButton("download", ICONS.download, "図をSVGで保存") +
    iconButton("source", ICONS.source, "記述と図を切り替える");
  return (
    `<figure class="diagram" data-diagram="${escapeHtml(source)}">` +
    `<div class="figure-toolbar">${toolbar}</div>` +
    `<div class="diagram-view"></div>` +
    `<pre class="code-block diagram-code"><code>${escapeHtml(source)}</code></pre>` +
    `</figure>`
  );
}

const LIST_ITEM = /^(\s*)(?:[-*+]|\d+[.)])\s+(.*)$/;
const ORDERED_ITEM = /^\s*\d+[.)]\s+/;
const indentOf = (line: string) => line.length - line.trimStart().length;

/** 項目の中身を組み立てる。文字どうしは改行で繋ぎ、入れ子リストはそのまま並べる。 */
const joinItemParts = (parts: { block: boolean; html: string }[]) =>
  parts
    .map((part, index) => (index > 0 && !part.block && !parts[index - 1].block ? "<br>" : "") + part.html)
    .join("");

/**
 * 箇条書きを、入れ子と継続行を含めて1つのブロックとして取り出す。
 *
 * 平坦に読むと「- 親 / 　- 子」が兄弟になり、**手順の親子関係が消える。**
 * 継続行（項目の下に字下げして書かれた説明）も、以前はリストを分断して
 * 独立した段落になっていた（どちらも2026-08-11に確認）。引き継ぎ資料への
 * 質問は手順を聞くものが多く、構造がそのまま答えの一部になる。
 *
 * **どの経路でも必ず1行以上進む。** 進まない経路を作ると、表のときと同じく
 * 描画が止まらなくなり、エラー境界では受け止められない（docs/08 M35）。
 */
function listBlock(lines: string[], start: number): { html: string; next: number } | null {
  const head = lines[start].match(LIST_ITEM);
  if (!head) return null;
  const indent = head[1].length;
  const ordered = ORDERED_ITEM.test(lines[start]);
  // 手順の途中に説明の段落が挟まると、そこでリストが切れて番号が1へ戻る。
  // 「4. 次に〜」と書かれているのに画面が「1.」と出すと、手順の指示として壊れる
  const firstNumber = ordered ? Number(lines[start].match(/^\s*(\d+)/)?.[1] ?? 1) : 1;
  const items: string[] = [];
  let i = start;

  while (i < lines.length) {
    // 項目の間に空行があっても同じリストとして続ける。空行で切ると、
    // 説明を挟んだ箇条書きが番号1から振り直しになる
    let cursor = i;
    while (cursor < lines.length && !lines[cursor].trim()) cursor++;
    if (cursor >= lines.length) break;

    const item = lines[cursor].match(LIST_ITEM);
    // 同じ深さ・同じ種類の項目だけをこのリストに入れる。
    // より深い項目は下の入れ子側で消費するので、ここへは残らない
    if (!item || item[1].length !== indent || ORDERED_ITEM.test(lines[cursor]) !== ordered) break;
    i = cursor + 1;

    const parts: { block: boolean; html: string }[] = [{ block: false, html: inline(item[2]).html }];
    for (;;) {
      let probe = i;
      while (probe < lines.length && !lines[probe].trim()) probe++;
      if (probe >= lines.length) break;

      if (LIST_ITEM.test(lines[probe])) {
        if (indentOf(lines[probe]) <= indent) break; // 同じか浅い ＝ この項目は終わり
        const nested = listBlock(lines, probe);
        if (!nested) break;
        parts.push({ block: true, html: nested.html });
        i = nested.next;
        continue;
      }
      if (indentOf(lines[probe]) <= indent) break; // 字下げが無い ＝ リストの外
      parts.push({ block: false, html: inline(lines[probe].trim()).html });
      i = probe + 1;
    }

    items.push(`<li>${joinItemParts(parts)}</li>`);
  }

  if (items.length === 0) return null;
  const tag = ordered ? "ol" : "ul";
  const from = ordered && firstNumber > 1 ? ` start="${firstNumber}"` : "";
  return { html: `<${tag}${from}>${items.join("")}</${tag}>`, next: i };
}

export function renderMarkdown(source: string, sources: Citation[] = []): string {
  // inline() は多くの場所から呼ばれるので、引数で持ち回らずここで差し替える。
  // 同期的に組み立て終わるので、途中で別の回答の出典が混ざることはない
  citations = sources;
  const lines = source.replace(/\r\n?/g, "\n").split("\n");
  const out: string[] = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];

    if (!line.trim()) {
      i++;
      continue;
    }

    // コードが最優先。中の記号を記法として解釈しないため、他より先に取り切る
    const code = fencedCode(lines, i);
    if (code) {
      out.push(code.html);
      i = code.next;
      continue;
    }

    const math = blockMath(lines, i);
    if (math) {
      out.push(math.html);
      i = math.next;
      continue;
    }

    // 表: ヘッダ行 + 区切り行 が揃っているときだけ表として扱う
    if (line.trim().startsWith("|") && isDivider(lines[i + 1] ?? "")) {
      const align = alignmentsOf(lines[i + 1]);
      const head = cells(line)
        .map((c, column) => `<th${align[column] ?? ""}>${inline(c).html}</th>`)
        .join("");
      i += 2;
      const body: string[] = [];
      while (i < lines.length && lines[i].trim().startsWith("|")) {
        const row = cells(lines[i])
          .map((c, column) => `<td${align[column] ?? ""}>${inline(c).html}</td>`)
          .join("");
        body.push(`<tr>${row}</tr>`);
        i++;
      }
      // CSVでの保存を添える。表のまま読むより、数字を手元で並べ替えたい場面がある
      out.push(
        `<figure class="data-table">` +
          `<div class="table-scroll"><table><thead><tr>${head}</tr></thead>` +
          `<tbody>${body.join("")}</tbody></table></div>` +
          `<div class="figure-actions">${iconButton("download-csv", ICONS.download, "表をCSVで保存")}</div>` +
          `</figure>`,
      );
      continue;
    }

    const heading = line.match(/^(#{1,6})\s+(.*)$/);
    if (heading) {
      const level = Math.min(heading[1].length + 2, 6); // 本文中なのでh3始まりにする
      out.push(`<h${level}>${inline(heading[2]).html}</h${level}>`);
      i++;
      continue;
    }

    const list = listBlock(lines, i);
    if (list) {
      out.push(list.html);
      i = list.next;
      continue;
    }

    if (/^>\s?/.test(line)) {
      const quote: string[] = [];
      while (i < lines.length && /^>\s?/.test(lines[i])) {
        quote.push(inline(lines[i].replace(/^>\s?/, "")).html);
        i++;
      }
      out.push(`<blockquote>${quote.join("<br>")}</blockquote>`);
      continue;
    }
    if (/^(-{3,}|\*{3,}|_{3,})$/.test(line.trim())) {
      out.push("<hr>");
      i++;
      continue;
    }

    // 段落: 空行までをひとまとまりにする
    const para: string[] = [];
    while (
      i < lines.length &&
      lines[i].trim() &&
      !LIST_ITEM.test(lines[i]) &&
      !FENCE_OPEN.test(lines[i]) &&
      !lines[i].trim().startsWith("|") &&
      !/^#{1,6}\s/.test(lines[i]) &&
      !/^>\s?/.test(lines[i])
    ) {
      para.push(inline(lines[i]).html);
      i++;
    }

    // **ここで必ず1行は進める。**
    //
    // どの分岐にも当てはまらず、段落としても1行も取れない行が存在する。
    // 代表例が「表の1行目だけが届いた状態」である。`| 項目 | 40代 |` は
    // 表の分岐で区切り行（`|---|---|`）が無いために弾かれ、段落の条件でも
    // `|` 始まりとして除外されるため、`i` が進まないまま無限ループになる。
    //
    // 回答はSSEで少しずつ届くので、**表を含む回答では必ずこの状態を通る。**
    // 空の段落を延々と積んでメモリを食い潰し、タブが固まって落ちていた
    // （2026-08-10に再現を確認）。個別の条件を直すのではなくここで進行を
    // 保証するのは、分岐条件と段落条件が食い違う組み合わせを将来また
    // 作り込んでも、無限ループにはならないようにするためである。
    if (para.length === 0) {
      para.push(inline(lines[i]).html);
      i++;
    }
    out.push(`<p>${para.join("<br>")}</p>`);
  }

  return out.join("");
}

/**
 * 本文が変わらない限り描き直さない。
 *
 * 回答はSSEで少しずつ届き、そのたびに会話全体が再描画される。memo が無いと
 * **すでに完成している過去の回答まで、届くたびに解析し直してDOMを丸ごと
 * 差し替える**ことになる。20往復の会話では1デルタあたり1.49msの解析が乗り、
 * 1回の回答で約298ms（実測、手元のMacで）。スマホではこの数倍かかる。
 *
 * text は文字列なので、既定の浅い比較でそのまま効く。sources は配列なので、
 * 毎回新しい配列を渡すと memo が効かない。**呼び出し側で turn.sources を
 * そのまま渡すこと**（同じ回答の間は同一参照のままである）。
 */
export const Markdown = memo(function Markdown(
  { text, sources }: { text: string; sources?: Citation[] },
) {
  const host = useRef<HTMLDivElement>(null);
  // renderMarkdown はエスケープ済みの文字列しか組み立てないため、
  // ここで生HTMLとして差し込んでも外部由来のタグは通らない
  const html = renderMarkdown(text, sources);

  // 図はHTMLを差し込んだ**後**に描く。本文が変わるたびにReactが innerHTML を
  // 作り直すので、描いたSVGはそのたびに消える。diagram.ts が原文で覚えているため、
  // 2回目以降は読み込みも解析もせずに戻る（回答は1文字ずつ届く＝この経路を何度も通る）
  useEffect(() => {
    void renderDiagrams(host.current);
  }, [html]);

  // ボタンは生成したHTMLの中にあるので、React側では受け取れない。
  // 1か所で拾って振り分ける（図・表の数だけハンドラを作らずに済む）
  const onClick = (event: MouseEvent<HTMLDivElement>) => {
    const button = (event.target as HTMLElement).closest<HTMLElement>("[data-figure-action]");
    if (!button) return;
    const figure = button.closest<HTMLElement>("figure");
    if (!figure) return;
    switch (button.dataset.figureAction) {
      case "copy":
        void copyText(figure.dataset.diagram ?? "");
        break;
      case "zoom-in":
        zoomDiagram(figure, +1);
        break;
      case "zoom-out":
        zoomDiagram(figure, -1);
        break;
      case "download":
        downloadDiagram(figure);
        break;
      case "source":
        figure.classList.toggle("is-source");
        break;
      case "download-csv":
        downloadTableCSV(figure);
        break;
    }
  };

  return (
    <div
      className="prose"
      ref={host}
      onClick={onClick}
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
});
