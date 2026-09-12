import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const page = readFileSync(new URL("../src/App.tsx", import.meta.url), "utf8");
const config = readFileSync(new URL("../src/config.ts", import.meta.url), "utf8");
const styles = readFileSync(new URL("../src/styles.css", import.meta.url), "utf8");

test("EnterとShift Enterは送信せず改行し、送信ボタンだけで送る", () => {
  assert.doesNotMatch(page, /onKeyDown=\{\(event\).*event\.key === "Enter"/s);
  assert.doesNotMatch(page, /composer-hint/);
  assert.match(page, /<button\s+type="submit"\s+className="send"/s);
});

test("回答完了後は出典カードを重ねて表示しない", () => {
  assert.match(page, /<ReferenceSummary sources=\{turn\.sources\} active=\{turn\.streaming\}/);
  assert.doesNotMatch(page, /className="sources"|参照中の資料/);
});

test("入力欄は5行まで自動で伸び、その後だけ内部スクロールする", () => {
  assert.match(config, /composerVisibleLines: 5/);
  assert.match(page, /resizeComposerTextarea\(event\.currentTarget\)/);
  assert.match(page, /target\.style\.overflowY = .* \? "auto" : "hidden"/);
  assert.match(styles, /\.composer textarea\s*\{[\s\S]*overflow-y: hidden/);
});

test("入力欄の案内は質問することだけを簡潔に示す", () => {
  assert.match(page, /placeholder="引き継ぎ資料について質問する"/);
  assert.doesNotMatch(page, /画像は貼り付けもできます/);
});

test("未入力時も入力後も質問文を入力欄の中央へ置く", () => {
  assert.match(styles, /\.composer\s*\{[^}]*align-items: center;/);
  assert.match(styles, /\.composer textarea\s*\{[^}]*padding: 0 2px;[^}]*line-height: 1\.5;/);
});

// 会話と入力欄で左右の余白・列幅を別々に書くと、右寄せの吹き出しの右端が
// 入力欄と揃わない。実測では**スマホで4px内側、PC（1020px以上）で20px外側**に
// ずれていた（2026-09-12）。同じ変数を使うこと自体をここで固定する。
test("会話と入力欄は同じ列幅・同じ左右余白を使う", () => {
  assert.match(styles, /--chat-column:\s*900px/);
  assert.match(styles, /--chat-gutter:\s*clamp\(20px, 6vw, 80px\)/);
  assert.match(styles, /\.conversation\s*\{[^}]*padding:\s*38px var\(--chat-gutter\) 28px/s);
  assert.match(styles, /\.composer-area\s*\{[^}]*padding:\s*48px var\(--chat-gutter\) 10px/s);
  for (const selector of [".conversation > \\*", ".composer-area > \\*"]) {
    assert.match(styles, new RegExp(`${selector}\\s*\\{[^}]*width:\\s*min\\(var\\(--chat-column\\), 100%\\)`, "s"));
  }
  // 片方だけを動かす書き方が戻っていないこと
  assert.doesNotMatch(styles, /\.(conversation|composer-area)\s*\{\s*padding-inline:/);
});
