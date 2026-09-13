import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const menu = readFileSync(new URL("../src/components/ToolMenu.tsx", import.meta.url), "utf8");
const page = readFileSync(new URL("../src/App.tsx", import.meta.url), "utf8");
const api = readFileSync(new URL("../src/api.ts", import.meta.url), "utf8");
const styles = readFileSync(new URL("../src/styles.css", import.meta.url), "utf8");
const admin = readFileSync(new URL("../src/admin/AdminPage.tsx", import.meta.url), "utf8");
const adminApi = readFileSync(new URL("../src/admin/api.ts", import.meta.url), "utf8");

test("入力欄の「+」から外部サービスとの連携をオン・オフできる", () => {
  assert.match(page, /<ToolMenu/);
  assert.match(menu, /tool-trigger/);
  assert.match(menu, /aria-label="外部サービスと連携"/);
  // スイッチの本体はチェックボックスのまま。キーボード操作と読み上げを標準に任せる
  assert.match(menu, /type="checkbox"/);
  assert.match(styles, /\.tool-switch input:checked \+ \.tool-switch-track/);
});

test("使えない参照先も一覧に出し、理由を書く", () => {
  // ⚠️ 黙って消すと「無い機能」に見え、設定すれば使えることが伝わらない
  assert.match(menu, /tool\.available \? tool\.description : tool\.reason/);
  assert.match(menu, /未接続/);
  assert.match(api, /reason\?: string/);
});

test("参照先はサーバーが決める（画面に固定で並べない）", () => {
  assert.match(api, /\$\{API_ORIGIN\}\/api\/tools/);
  assert.match(page, /refreshTools\(\)\]\)/);
  // 使えなくなったものをオンのまま残さない。表示と実際の参照先が食い違う
  assert.match(page, /current\.filter\(\(id\) => list\.some\(\(tool\) => tool\.id === id && tool\.available\)\)/);
});

test("選んだ参照先を質問と一緒に送り、次に開いたときも覚えている", () => {
  assert.match(api, /tools: enabledTools \?\? \[\]/);
  assert.match(page, /controller\.signal, assistantId, enabledTools,/);
  assert.match(page, /writeStored\("local", TOOLS_KEY/);
});

test("何かオンになっていることが、開かなくても分かる", () => {
  assert.match(menu, /className=\{`tool-trigger\$\{active\.length > 0 \? " is-active" : ""\}`\}/);
  assert.match(styles, /\.tool-trigger\.is-active/);
  assert.match(styles, /\.tool-badge/);
});

test("入力欄は最下部にあるので、一覧は上へ開く", () => {
  assert.match(styles, /\.tool-popover \{[\s\S]*?bottom: calc\(100% \+ 8px\)/);
});

test("回答中は参照先を変えられない（送信済みの質問と食い違う）", () => {
  assert.match(page, /<ToolMenu[\s\S]*?disabled=\{streaming\}/);
});

test("代ごとにDiscordのサーバーが変わるので、どれを読むか選べる", () => {
  // ⚠️ 設定で1つに固定しない。どの代の会話を読むかは利用者にしか決められない
  assert.match(api, /servers\?: \{ id: string; name: string \}\[\]/);
  assert.match(menu, /検索するDiscordサーバー/);
  assert.match(menu, /label: "すべてのサーバー"/);
  assert.match(api, /discordServer: discordServer \?\? ""/);
  assert.match(page, /writeStored\("local", DISCORD_SERVER_KEY, id\)/);
});

test("選んでいたサーバーが無くなったら「すべて」へ戻す", () => {
  // 消えたサーバーを指したままだと、検索しても毎回0件になる
  assert.match(page, /servers\.some\(\(server\) => server\.id === current\)/);
});

test("オンのとき、サーバーが2つ以上あるときだけ選ばせる", () => {
  // 選びようが無いものを並べても、設定が増えたように見えるだけ（2026-09-13の指摘）
  assert.match(menu, /enabled\.includes\(tool\.id\) && \(tool\.servers\?\.length \?\? 0\) > 1/);
});

test("起動時もログイン直後も、同じ関数で参照先を読み直す", () => {
  // ⚠️ 以前は2か所へ並べて書いており、参照先の一覧を片方だけに足していたため、
  // 一度ログインしたまま開き直すと「+」が消えていた（2026-09-13に発覚）
  assert.match(page, /async function restoreAfterSignIn\(\)/);
  assert.match(page, /Promise\.all\(\[restoreHistory\(\), refreshAssistants\(\), refreshTools\(\)\]\)/);
  // 呼び出しは2か所（起動時とログイン直後）。個別に並べ直さない
  const calls = page.match(/void restoreAfterSignIn\(\);/g) ?? [];
  assert.equal(calls.length, 2);
  assert.doesNotMatch(page, /void refreshTools\(\);/);
});

test("選択肢の見た目をOS任せにしない（画面と同じ部品を使う）", () => {
  // SelectMenu は「OSごとに選択肢だけ外観が変わるのを避ける」ための部品
  assert.match(menu, /import \{ SelectMenu \} from "\.\/SelectMenu"/);
  assert.doesNotMatch(menu, /<select/);
  assert.match(styles, /\.tool-item-server \.select-menu-trigger/);
});

test("サービスごとの印を左に出す", () => {
  assert.match(menu, /function ToolIcon/);
  assert.match(menu, /id === "discord"/);
  assert.match(menu, /id === "drive"/);
  // 知らないIDが来ても崩れない
  assert.match(menu, /<circle cx="12" cy="12" r="9"/);
  assert.match(styles, /\.tool-icon \{/);
});

test("説明は増やさず、必要なことだけ書く", () => {
  assert.match(menu, /外部サービスと連携<\/p>/);
  // 「置き場所を足すものです」の説明文は消した（画面が説明で埋まる）
  assert.doesNotMatch(menu, /引き継ぎ資料はいつでも読みます/);
  assert.doesNotMatch(styles, /\.tool-popover-note/);
});

test("外部サービスの連携状態を管理画面で見られる", () => {
  // 環境変数を読める人しか状態が分からない作りだと、代替わりのときに誰も直せない
  assert.match(adminApi, /\/api\/admin\/integrations/);
  assert.match(admin, /外部サービス連携/);
  assert.match(admin, /接続済み/);
  assert.match(admin, /未接続/);
});

test("ボットの追加を管理画面から押せる", () => {
  // コマンドを打てる人しか追加できない作りだと、代が替わると使えなくなる
  assert.match(admin, /link\.actionUrl/);
  assert.match(admin, /admin-link-action/);
  assert.match(styles, /\.admin-link-action \{/);
});

test("共有相手のアドレスは選んでコピーできる形で出す", () => {
  // 手で写すと打ち間違えて、原因の分からない404になる
  assert.match(admin, /共有相手に追加するアドレス/);
  assert.match(styles, /\.admin-link-share code \{[\s\S]*?user-select: all;/);
});

test("項目が増えても一覧が上へせり上がらない", () => {
  // ⚠️ 上へ伸びる作りなので、高さを止めないと読んでいた場所が動く（2026-09-13の指摘）
  assert.match(styles, /\.tool-popover \{[\s\S]*?max-height: min\(58vh, 420px\)/);
  assert.match(styles, /\.tool-popover > ul \{[\s\S]*?overflow-y: auto;/);
});

test("説明は2行までに収める", () => {
  // 3行4行と折り返すと、その項目だけ背が高くなって一覧全体が伸びる
  assert.match(styles, /\.tool-item-note \{[\s\S]*?-webkit-line-clamp: 2;/);
});
