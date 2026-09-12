/**
 * 回答の中のMermaidの図と、表への操作（コピー・拡大縮小・保存）を扱う。
 *
 * # なぜ markdown.tsx から切り離してあるか
 *
 * markdown.tsx は「**先にすべてエスケープしてから整形する**ので、生のHTMLが
 * 通る経路が構造的に存在しない」という作りになっている。Mermaidはこれの
 * 唯一の例外で、テキストからSVGを組み立てて差し込む。**例外を1ファイルに
 * 閉じ込めておかないと、あの説明がいつの間にか嘘になる。**
 *
 * 安全側の措置は2つある。
 *
 *  1. `securityLevel: "strict"` を指定する（ラベル中のHTMLとクリック操作を
 *     Mermaid側で無効にする）
 *  2. 描画に失敗したら**コードブロックのまま残す**。図が出ないことはあっても、
 *     図に書かれていた情報そのものが消えることはない
 *
 * # なぜ動的importなのか
 *
 * Mermaidは初回JS（2026-08-11時点で252kB）と比べて桁違いに大きい。
 * 常に読み込むと、図が1つも出ない大多数の質問まで遅くなる。
 * **実際に ```mermaid が本文へ現れたときだけ**読み込む。
 */

/** Mermaidの `render` だけを使う。型のためだけに本体をimportしない（動的importの意味が消える） */
type MermaidAPI = {
  initialize: (config: Record<string, unknown>) => void;
  render: (id: string, source: string) => Promise<{ svg: string }>;
};

let loading: Promise<MermaidAPI | null> | null = null;

/**
 * 描いた結果を原文で覚えておく。
 *
 * 回答はSSEで少しずつ届き、1デルタごとにReactが innerHTML を作り直すため、
 * **図を含む回答では描画要求が何十回も来る。** Mermaidの解析は1枚で数十msかかる
 * ので、覚えていないとそのぶん本文の描画が止まる（docs/08 M15と同じ性質の問題）。
 */
const drawn = new Map<string, string>();

/** SVGの id は文書内で衝突してはいけない。Mermaidが内部の参照に使う */
let sequence = 0;

function load(): Promise<MermaidAPI | null> {
  loading ??= import("mermaid")
    .then((module) => {
      const mermaid = module.default as unknown as MermaidAPI;
      mermaid.initialize({
        startOnLoad: false,
        // ラベルへHTMLを書かせない。回答本文は生成物なので、図のラベルにも
        // 何が入るか保証できない
        securityLevel: "strict",
        theme: "default",
        fontFamily: "inherit",
        flowchart: { htmlLabels: false, useMaxWidth: true },
      });
      return mermaid;
    })
    // 読み込めなくてもコードは残っている。ここで投げても誰も受け取れない
    .catch(() => null);
  return loading;
}

/** 図を本文へ描く。`figure.diagram` を探して、まだ描いていないものだけ処理する。 */
export async function renderDiagrams(root: HTMLElement | null): Promise<void> {
  if (!root) return;
  const figures = Array.from(root.querySelectorAll<HTMLElement>("figure.diagram[data-diagram]"));
  if (figures.length === 0) return;

  // 覚えているものは先に戻す。読み込みを待たないので、描き直しでもちらつかない
  const pending = figures.filter((figure) => {
    const cached = drawn.get(figure.dataset.diagram ?? "");
    if (!cached) return true;
    paint(figure, cached);
    return false;
  });
  if (pending.length === 0) return;

  const mermaid = await load();
  if (!mermaid) return;

  for (const figure of pending) {
    const source = figure.dataset.diagram ?? "";
    // 待っている間に本文が差し替わって、この要素が捨てられていることがある
    if (!figure.isConnected || figure.classList.contains("is-rendered")) continue;
    try {
      const { svg } = await mermaid.render(`diagram-${++sequence}`, source);
      drawn.set(source, svg);
      paint(figure, svg);
    } catch {
      // 構文が壊れている図。コードブロックのまま残す（エラー文は出さない。
      // 利用者にはどうにもできないうえ、本文の途中に赤い箱が出るほうが邪魔）
      figure.classList.add("is-broken");
    }
  }
}

function paint(figure: HTMLElement, svg: string): void {
  const view = figure.querySelector<HTMLElement>(".diagram-view");
  if (!view) return;
  view.innerHTML = svg;
  const element = view.querySelector("svg");
  if (element) {
    // Mermaidはsvgへ `max-width` を直接書く。インラインのほうが強いので、
    // 拡大してもそこで頭打ちになる。拡大縮小はCSS側に任せたいので外す
    element.style.removeProperty("max-width");
    // **本来の大きさをpxで固定する。**
    //
    // 以前はCSSで幅を100%にしていたが、`flowchart TD` のような縦長の図では
    // 横幅に合わせて引き伸ばされ、ノード1個が画面いっぱいになっていた
    // （2026-09-12に本番で確認）。viewBoxの寸法がその図の本来の大きさなので、
    // それを基準にし、収まらないぶんは枠の中でスクロールさせる。
    const box = element.getAttribute("viewBox")?.split(/[\s,]+/);
    if (box?.length === 4 && Number(box[2]) > 0) {
      element.style.width = `${Number(box[2])}px`;
      element.style.height = "auto";
      element.removeAttribute("height"); // 属性が残ると縦横比が崩れる
    }
  }
  figure.classList.add("is-rendered");
}

const ZOOM_STEPS = [0.5, 0.75, 1, 1.5, 2, 3];

/** 図の拡大縮小。段階は決め打ちで、連続した値は持たない（元の倍率へ戻せなくなる） */
export function zoomDiagram(figure: HTMLElement, direction: 1 | -1): void {
  const current = Number(figure.dataset.zoom ?? "1");
  const index = ZOOM_STEPS.indexOf(current);
  const next = ZOOM_STEPS[Math.min(Math.max((index < 0 ? 2 : index) + direction, 0), ZOOM_STEPS.length - 1)];
  figure.dataset.zoom = String(next);
  figure.style.setProperty("--diagram-zoom", String(next));
}

export async function copyText(text: string): Promise<void> {
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    // 権限が無い、またはhttpsでない。押しても何も起きないだけにする
  }
}

/** 保存はBlobで行う。外部へ送らないので、通信もサーバー側の対応も要らない */
function save(name: string, type: string, body: string): void {
  const url = URL.createObjectURL(new Blob([body], { type }));
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  link.click();
  // 即座に外すと保存が始まらない端末がある
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
}

const stamp = () => new Date().toISOString().slice(0, 10).replace(/-/g, "");

export function downloadDiagram(figure: HTMLElement): void {
  const svg = figure.querySelector(".diagram-view svg");
  if (!svg) return;
  save(`wasa-chat-図-${stamp()}.svg`, "image/svg+xml;charset=utf-8", svg.outerHTML);
}

/**
 * 表をCSVで保存する。
 *
 * **先頭にBOMを付ける。** 付けないとExcelがUTF-8と判断せず、日本語の見出しが
 * 文字化けする。部内での用途はほぼExcelなので、ここは既定で付けてよい。
 */
export function downloadTableCSV(figure: HTMLElement): void {
  const rows = Array.from(figure.querySelectorAll("tr")).map((row) =>
    Array.from(row.querySelectorAll("th, td"))
      .map((cell) => {
        const text = (cell.textContent ?? "").trim();
        // 引用符・カンマ・改行を含むセルは囲む。囲んだ中の `"` は2つにする
        return /[",\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
      })
      .join(","),
  );
  if (rows.length === 0) return;
  save(`wasa-chat-表-${stamp()}.csv`, "text/csv;charset=utf-8", "﻿" + rows.join("\r\n"));
}
