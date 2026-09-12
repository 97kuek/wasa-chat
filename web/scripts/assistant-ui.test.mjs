import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const page = readFileSync(new URL("../src/App.tsx", import.meta.url), "utf8");
const styles = readFileSync(new URL("../src/styles.css", import.meta.url), "utf8");
const avatar = readFileSync(new URL("../src/avatar.tsx", import.meta.url), "utf8");

// **作成者でもまず閲覧から始める。** 設定を見に来ただけのときに編集欄が開いていると、
// 触るつもりのない値を書き換えてしまう。編集は「編集する」から明示的に入る。
test("アシスタントは閲覧で開き、編集は明示的に入る", () => {
  assert.match(page, /setAssistantForm\(\{ mode: "view", assistantId: item\.id \}\)/);
  assert.match(page, /function startEditFromView\(\)/);
  assert.match(page, /assistantFormReadOnly && formAssistant\?\.canEdit/);
  assert.match(page, /readOnly=\{assistantFormReadOnly\}/);
  assert.match(page, /onClick=\{\(\) => openAssistantSettings\(item\)\}>設定<\/button>/);
  assert.match(page, /複製して作る/);
});

// 設定する項目は多くない。タブで分けると全部見るのに4回切り替えることになるので、
// 1画面に並べて画面の広さに応じて段を増やす。
test("アシスタント設定は1画面に並べ、横幅を制限しない", () => {
  for (const heading of ["基本設定", "参照範囲", "指示（口調・書き方）", "用語集"]) {
    assert.match(page, new RegExp(`<h3>${heading.replace(/[()（）]/g, (c) => "\\" + c)}</h3>`));
  }
  assert.match(page, /className="assistant-sections"/);
  assert.doesNotMatch(page, /assistantTab|assistant-tab/);
  assert.match(styles, /\.assistant-sections\s*\{[^}]*grid-template-columns: repeat\(auto-fit, minmax\(340px, 1fr\)\)/s);
  assert.doesNotMatch(styles, /\.assistant-page \.assistant-form\s*\{[^}]*max-width/s);
});

// 黒背景・白背景・青文字の3種類が混ざっていた。主操作は黒、それ以外は白の2つに揃える。
test("設定画面のボタンは1組の見た目に揃える", () => {
  assert.doesNotMatch(page, /assistant-edit-enter/);
  assert.doesNotMatch(styles, /assistant-edit-enter/);
  assert.match(styles, /\.assistant-form-head button,\n\.assistant-actions button \{/);
  assert.match(styles, /\.assistant-form-head button\.primary,\n\.assistant-actions button\.primary \{/);
});

// 指示は1本の文字列が正本で、4つの欄はその見え方にすぎない。
// 状態を2つ持つと必ずずれるため、毎回 instruction から読み直す。
test("指示は4つの欄に分け、保存する形は1本の文字列のままにする", () => {
  assert.match(page, /const instructionParts = splitInstruction\(assistantDraft\.instruction\)/);
  assert.match(page, /instruction: joinInstruction\(\{ \.\.\.parts, \[key\]: value \}\)/);
  for (const heading of ["役割", "口調", "出力の形", "やらないこと"]) {
    assert.match(page, new RegExp(`heading: "${heading}"`));
  }
  // 型に沿っていない既存の指示は分解しない（文章が並べ替わると意図が変わる）
  assert.match(page, /指示（自由記述）/);
});

// 用語集は語の言い換えを置く場所であって、事実を置く場所ではない。
test("用語集は事実を置く場所ではないと画面にも書く", () => {
  assert.match(page, /事実を書く場所ではありません/);
  assert.match(page, /出典が付かない/);
  assert.match(page, /APP_LIMITS\.glossaryEntries/);
});

// 0件になる組み合わせ（公式サイト×電装班など）を選んでも、これが無いと
// 質問するまで気付けない。
test("参照範囲は選んだ結果の件数を出す", () => {
  assert.match(page, /const scopeCount = scopeCounts\[`\$\{assistantDraft\.origin \?\? ""\}\/\$\{assistantDraft\.team \?\? ""\}`\]/);
  assert.match(page, /この条件で参照できる資料/);
  assert.match(page, /0件です。範囲を広げてください/);
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
