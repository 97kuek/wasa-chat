import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const page = readFileSync(new URL("../src/App.tsx", import.meta.url), "utf8");
const styles = readFileSync(new URL("../src/styles.css", import.meta.url), "utf8");
const avatar = readFileSync(new URL("../src/avatar.tsx", import.meta.url), "utf8");
// 設定フォームは App.tsx から切り出してある（2026-09-12）。
const form = readFileSync(new URL("../src/assistant/SettingsForm.tsx", import.meta.url), "utf8");

// **作成者でもまず閲覧から始める。** 設定を見に来ただけのときに編集欄が開いていると、
// 触るつもりのない値を書き換えてしまう。編集は「編集する」から明示的に入る。
test("アシスタントは閲覧で開き、編集は明示的に入る", () => {
  assert.match(page, /setAssistantForm\(\{ mode: "view", assistantId: item\.id \}\)/);
  assert.match(page, /function startEditFromView\(\)/);
  assert.match(form, /readOnly && assistant\?\.canEdit/);
  assert.match(page, /readOnly=\{assistantFormReadOnly\}/);
  assert.match(page, /onClick=\{\(\) => openAssistantSettings\(item\)\}>設定<\/button>/);
  assert.match(form, /複製して作る/);
});

// 設定する項目は多くない。タブで分けると全部見るのに4回切り替えることになるので、
// 1画面に並べて画面の広さに応じて段を増やす。
test("アシスタント設定は1画面に並べ、横幅を制限しない", () => {
  for (const heading of ["基本設定", "参照範囲", "指示（口調・書き方）", "用語集"]) {
    assert.match(form, new RegExp(`<h3>${heading.replace(/[()（）]/g, (c) => "\\" + c)}</h3>`));
  }
  assert.match(form, /className="assistant-sections"/);
  assert.doesNotMatch(page, /assistantTab|assistant-tab/);
  assert.doesNotMatch(styles, /\.assistant-page \.assistant-form\s*\{[^}]*max-width/s);
});

// 高さの違う小さな箱が横に散らばると読みにくい。枠で囲まず、段は1か2までにする。
test("設定の節は枠で囲まず、段は最大2つにする", () => {
  assert.doesNotMatch(styles, /\.assistant-section\s*\{[^}]*border:/s);
  assert.match(styles, /\.assistant-sections\s*\{[^}]*grid-template-columns: minmax\(0, 1fr\)/s);
  assert.match(styles, /@media \(min-width: 1000px\)\s*\{\s*\.assistant-sections \{ grid-template-columns: repeat\(2, minmax\(0, 1fr\)\); \}/);
});

// 黒背景・白背景・青文字の3種類が混ざっていた。主操作は黒、それ以外は白の2つに揃える。
test("設定画面のボタンは1組の見た目に揃える", () => {
  assert.doesNotMatch(form, /assistant-edit-enter/);
  assert.doesNotMatch(styles, /assistant-edit-enter/);
  assert.match(styles, /\.assistant-form-head button,\n\.assistant-actions button \{/);
  assert.match(styles, /\.assistant-form-head button\.primary,\n\.assistant-actions button\.primary \{/);
});

// 指示は1本の文字列が正本で、4つの欄はその見え方にすぎない。
// 状態を2つ持つと必ずずれるため、毎回 instruction から読み直す。
test("指示は4つの欄に分け、保存する形は1本の文字列のままにする", () => {
  assert.match(form, /const instructionParts = splitInstruction\(draft\.instruction\)/);
  assert.match(form, /instruction: joinInstruction\(\{ \.\.\.parts, \[key\]: value \}\)/);
  for (const heading of ["役割", "口調", "出力の形", "やらないこと"]) {
    assert.match(form, new RegExp(`heading: "${heading}"`));
  }
  // 型に沿っていない既存の指示は分解しない（文章が並べ替わると意図が変わる）
  assert.match(form, /指示（自由記述）/);
});

// 用語集は語の言い換えを置く場所であって、事実を置く場所ではない。
test("用語集は事実を置く場所ではないと画面にも書く", () => {
  assert.match(form, /事実を書く場所ではありません/);
  assert.match(form, /出典が付かない/);
  assert.match(form, /APP_LIMITS\.glossaryEntries/);
});

// 0件になる組み合わせ（公式サイト×電装班など）を選んでも、これが無いと
// 質問するまで気付けない。
test("参照範囲は選んだ結果の件数を出す", () => {
  assert.match(form, /const scopeCount = scopeCounts\[`\$\{draft\.origin \?\? ""\}\/\$\{draft\.team \?\? ""\}`\]/);
  assert.match(form, /この条件で参照できる資料/);
  assert.match(form, /0件です。範囲を広げてください/);
});

test("回答横のアシスタントアイコンから設定を開く", () => {
  assert.match(page, /className="assistant-avatar-settings"/);
  assert.match(page, /aria-label=\{`「\$\{item\.name\}」の設定を見る`\}/);
  assert.match(page, /onClick=\{\(\) => openAssistantSettings\(item\)\}/);
});

test("アイコン画像は位置調整UIを持たず中央で切り抜く", () => {
  assert.doesNotMatch(page, /画像の位置|iconPosition|assistant-icon-position/);
  assert.doesNotMatch(avatar, /IconCropPosition|clampPercentage/);
  assert.match(avatar, /const sourceX = \(bitmap\.width - side\) \/ 2;/);
  assert.match(avatar, /const sourceY = \(bitmap\.height - side\) \/ 2;/);
});

// 出所はサーバー側（assistant.origins）が正本。画面の選択肢が欠けていると、
// サーバーは受け付けるのに画面から選べない状態になる（fee が実際そうだった）。
test("参照範囲の選択肢はサーバーが受け付ける出所と揃える", () => {
  const api = readFileSync(new URL("../src/api.ts", import.meta.url), "utf8");
  assert.match(api, /export type AssistantOrigin = "wiki" \| "site" \| "fee";/);
  for (const value of ["wiki", "site", "fee"]) {
    assert.match(form, new RegExp(`value: "${value}"`));
  }
  // 画面がunionを直書きすると、出所を足したときに拾い漏れる
  assert.doesNotMatch(api, /origin\?: "wiki" \| "site"/);
});

// Wiki以外をまとめて「公式サイト」と表示していたため、フライトシミュレータの
// 差分も公式サイトとして出ていた。
test("管理画面の出所名はGoのOriginLabelと揃える", () => {
  const admin = readFileSync(new URL("../src/admin/AdminPage.tsx", import.meta.url), "utf8");
  assert.match(admin, /if \(source === "fee"\) return "フライトシミュレータ";/);
  assert.doesNotMatch(admin, /source === "wiki" \? "Wiki" : "公式サイト"/);
});
