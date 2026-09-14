import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const settings = readFileSync(new URL("../src/settings/SettingsPage.tsx", import.meta.url), "utf8");
const icon = readFileSync(new URL("../src/components/ToolIcon.tsx", import.meta.url), "utf8");
const page = readFileSync(new URL("../src/App.tsx", import.meta.url), "utf8");
const api = readFileSync(new URL("../src/api.ts", import.meta.url), "utf8");
const styles = readFileSync(new URL("../src/styles.css", import.meta.url), "utf8");
const admin = readFileSync(new URL("../src/admin/AdminPage.tsx", import.meta.url), "utf8");
const adminApi = readFileSync(new URL("../src/admin/api.ts", import.meta.url), "utf8");
// ⚠️ 画面から外した説明の置き場所。**消えていないことをここで見張る**
const support = readFileSync(new URL("../public/support.html", import.meta.url), "utf8");

test("外部サービス連携は設定画面で行う（入力欄の「+」ではない）", () => {
  // ⚠️ 連携は質問のたびに切り替えるものではなく、つないだら続くもの。
  // 「+」のままだと、つなぐ・外す・つなぎ替えるという操作の置き場所が無い
  assert.match(page, /<SettingsPage/);
  assert.match(page, /view === "settings"/);
  // 押した一覧をその場で開く部品は無くした
  assert.doesNotMatch(page, /ToolMenu/);
  assert.doesNotMatch(styles, /\.tool-popover/);
});

test("設定画面はURLを持ち、戻るボタンでチャットへ帰れる", () => {
  assert.match(page, /history\.pushState\(null, "", "\/settings"\)/);
  assert.match(page, /location\.pathname === "\/settings"/);
  // 出口は1か所にまとめる（チャットへ戻る道は複数あり、各所へ書くと漏れる）
  assert.match(page, /view !== "settings" && location\.pathname === "\/settings"/);
});

test("連携はアカウントに持つ（端末に覚えない）", () => {
  // 端末ごとだと、別の端末で開いたときにつないだはずの連携が黙って外れる
  assert.match(api, /\$\{API_ORIGIN\}\/api\/settings/);
  assert.match(api, /export async function saveSettings/);
  assert.doesNotMatch(page, /TOOLS_KEY|DISCORD_SERVER_KEY/);
});

test("質問と一緒に参照先を送らない（サーバーが保存先から読む）", () => {
  // 送る作りだと、設定を変えた直後の質問が古い指定のまま飛ぶ
  assert.doesNotMatch(api, /tools: enabledTools/);
  assert.doesNotMatch(api, /discordServer:/);
  assert.match(page, /controller\.signal, assistantId, context, responseMode/);
});

test("Discordのサーバーは連携・解除・つなぎ替えができる", () => {
  // ⚠️ 代ごとにサーバーが変わり、つなぐ先は人によって違う（2026-09-14の指摘）
  assert.match(settings, /検索する<\/h4>/);
  assert.match(settings, /追加できる<\/h4>/);
  assert.match(settings, /onConnectServer/);
  assert.match(settings, /onDisconnectServer/);
  assert.match(page, /function connectDiscordServer/);
  assert.match(page, /function disconnectDiscordServer/);
});

test("Discordにオン・オフのスイッチを置かない", () => {
  // 連携したサーバーの有無がそのままオン・オフ。両方を持つと
  // 「オンなのに1つも連携していない」＝黙って何も読まない状態が作れる
  assert.match(api, /connected: DiscordServer\[\]/);
  assert.doesNotMatch(api, /discord[\s\S]{0,80}enabled: boolean/);
  assert.match(page, /settings\.discord\.connected\.length > 0/);
});

test("⚠️ 検索先の設定と、Discord側のボットの設定を混ぜない", () => {
  // 「解除」で /wasa まで止まると思われると、止めたつもりで使われ続ける。
  // ⚠️ **説明は画面に置かない**（2026-09-14の指摘で、畳んだ補足ごと外した）。
  // 置き場所はサポートページで、**消えていないことをここで見張る**
  assert.match(support, /Discordの「連携」と、Discord上のコマンドは別物です/);
  assert.match(support, /連携を解除しても止まりません/);
  assert.match(support, /ボットを退出させてください/);
  // 画面で言うのは、何をするかの1行だけ
  assert.match(settings, /公開チャンネルの会話を検索します/);
});

test("説明文を画面に置かない", () => {
  // ⚠️ 読むものが増えるほど、何をする画面か分からなくなる（2026-09-14の指摘）。
  // 手順や注意は畳んでも要らないと判断された。**details ごと置かない**
  const body = settings.slice(settings.indexOf("return ("));
  assert.doesNotMatch(body, /<details/);
  const lines = [...body.matchAll(/<p[^>]*>([\s\S]*?)<\/p>/g)]
    .map((match) => match[1].replace(/\{[^}]*\}|<[^>]+>/g, "").replace(/\s+/g, "").trim())
    .filter(Boolean);
  const longest = Math.max(...lines.map((line) => line.length));
  const total = lines.reduce((sum, line) => sum + line.length, 0);
  assert.ok(longest <= 40, `1行が長すぎる（${longest}字）: ${lines.find((l) => l.length === longest)}`);
  assert.ok(total <= 120, `画面の説明が多すぎる（合計${total}字）`);
});

test("ボットをサーバーへ入れる入口を画面に出す", () => {
  // コマンドを打てる人しか増やせない作りだと、代が替わると使えなくなる
  assert.match(settings, /ボットをサーバーに入れる/);
  assert.match(settings, /inviteUrl/);
  assert.match(styles, /\.settings-invite \{/);
  // 手順（サーバー管理権限が要ることなど）はサポートページに置く
  assert.match(support, /サーバー管理/);
});

test("入れた直後に出てこないので、一覧を取り直せる", () => {
  // サーバー側が一覧を10分覚えている。取り直せないと入れ直しを試させる
  assert.match(settings, /一覧を更新/);
  assert.match(api, /refresh \? "\?refresh=1" : ""/);
  assert.match(page, /async function refreshDiscordServers/);
});

test("連携できる数の上限を、押せない理由として書く", () => {
  // 書かないと、押せないボタンが壊れているように見える
  assert.match(settings, /maxServers/);
  assert.match(settings, /\{maxServers\}サーバーまで/);
});

test("⚠️ 連携は「自分だけが読む」設定ではない、と説明が残っている", () => {
  // docs/09 A-12。ボットを入れたサーバーの公開チャンネルは、ログインできる人なら
  // 誰でも連携して読める。**画面からは外したので、サポートページで見張る**
  assert.match(support, /誰でも連携して読めます/);
  // 読む範囲（公開チャンネルだけ）は、画面ではカード下の1行が兼ねる
  assert.match(settings, /公開チャンネルの会話を検索します/);
});

test("どのサーバーか、名前以外でも見分けが付く", () => {
  // ⚠️ 「WASA 41代」「WASA 42代」と4つ並んでも、初めて設定する人には
  // どれが自分のいるサーバーか分からない（2026-09-14の指摘）
  assert.match(settings, /function ServerRow/);
  assert.match(settings, /server\.icon/);
  assert.match(settings, /\$\{server\.members\}人/);
  assert.match(settings, /公開\$\{server\.channels\}チャンネル/);
  assert.match(api, /icon\?: string/);
  assert.match(api, /members\?: number/);
  assert.match(api, /channels\?: number/);
  assert.match(styles, /\.settings-server-icon \{/);
  // アイコンが無いサーバーは頭文字で描く（画像が無いだけで行が崩れない）
  assert.match(settings, /is-blank/);
});

test("不明な数値を0として出さない", () => {
  // 「0人」「公開0チャンネル」と出ると、閉じているサーバーに見える
  assert.match(settings, /server\.members \? /);
  assert.match(settings, /server\.channels \? /);
});

test("使えない参照先も一覧に出し、理由を書く", () => {
  // ⚠️ 黙って消すと「無い機能」に見え、設定すれば使えることが伝わらない
  assert.match(settings, /tool\.available \? tool\.description : tool\.reason/);
  assert.match(settings, /未接続/);
  assert.match(api, /reason\?: string/);
});

test("参照先はサーバーが決める（画面に固定で並べない）", () => {
  assert.match(page, /async function refreshSettings\(\)/);
  assert.match(page, /Promise\.all\(\[restoreHistory\(\), refreshAssistants\(\), refreshSettings\(\)\]\)/);
  // 呼び出しは2か所（起動時とログイン直後）。個別に並べ直さない
  const calls = page.match(/void restoreAfterSignIn\(\);/g) ?? [];
  assert.equal(calls.length, 2);
});

test("スイッチの本体はチェックボックスのまま", () => {
  // キーボード操作と読み上げを標準に任せる
  assert.match(settings, /type="checkbox"/);
  assert.match(styles, /\.tool-switch input:checked \+ \.tool-switch-track/);
});

test("保存した結果で描き直す（画面の手元の値を正としない）", () => {
  // 上限超えやボットが入っていないサーバーはサーバー側が断る
  assert.match(api, /return body as Settings/);
  assert.match(page, /setSettings\(await saveSettings\(next\)\)/);
});

test("連携の印は入力欄の左、送信と添付は右", () => {
  // 参照先は文字を打つ前に決めるもので、送信・添付とは役割が違う（2026-09-13の指摘）。
  // ⚠️ 3列（印 / 入力欄 / 右の操作）。2列に戻すと印が右へ寄る
  assert.match(styles, /\.composer \{[\s\S]*?grid-template-columns: auto minmax\(0, 1fr\) auto;/);
  const composer = page.slice(page.indexOf('className="composer"'));
  assert.ok(
    composer.indexOf("tool-trigger") < composer.indexOf("<textarea"),
    "連携の印が入力欄より後ろにある",
  );
  assert.ok(
    composer.indexOf("<textarea") < composer.indexOf('className="composer-actions"'),
    "右の操作が入力欄より前にある",
  );
});

test("いま何をつないでいるかは、設定画面を開かなくても分かる", () => {
  // 黙って外部を読むのと、黙って読まないのはどちらも困る
  assert.match(page, /className=\{`tool-trigger\$\{connectedTools\.length > 0 \? " is-active" : ""\}`\}/);
  assert.match(page, /連携中: \$\{connectedTools\.map\(\(tool\) => tool\.name\)\.join\("・"\)\}/);
  assert.match(styles, /\.tool-trigger\.is-active/);
  // 押すと設定画面へ行く
  assert.match(page, /onClick=\{openSettings\}/);
});

test("サービスごとの印は1か所で描く", () => {
  // 2か所に描くと、同じサービスが画面によって違う形で出る
  assert.match(icon, /export function ToolIcon/);
  assert.match(icon, /id === "discord"/);
  assert.match(icon, /id === "drive"/);
  // 知らないIDが来ても崩れない
  assert.match(icon, /<circle cx="12" cy="12" r="9"/);
  assert.match(styles, /\.tool-icon \{/);
  assert.match(settings, /import \{ ToolIcon \}/);
  assert.match(page, /import \{ ToolIcon \}/);
});

test("GoogleドライブとカレンダーのSVGを目分量で書かない", () => {
  // ⚠️ 比率が合わないと、20px で出したときに別のサービスに見える（2026-09-13の指摘）
  assert.match(icon, /viewBox="0 0 87\.3 78"/); // 公式ロゴの座標系
  // カレンダーは日付を書く。色の塊だけだと何の印か伝わらない
  assert.match(icon, /<text[\s\S]*?31[\s\S]*?<\/text>/);
});

test("サーバー名を途中で切らない", () => {
  // 「WASA 42代」は代の数字が後ろにあり、切れるとどの代か分からなくなる
  assert.match(styles, /\.settings-server-name \{[^}]*overflow-wrap: anywhere;/);
  // 狭い画面では縦に積む（横のままだと長い名前でボタンが潰れる）
  const narrow = styles.slice(styles.indexOf("@media (max-width: 560px)"));
  assert.match(narrow, /\.settings-servers li \{ flex-wrap: wrap; \}/);
});

test("外部サービスの連携状態を管理画面でも見られる", () => {
  // 環境変数を読める人しか状態が分からない作りだと、代替わりのときに誰も直せない
  assert.match(adminApi, /\/api\/admin\/integrations/);
  assert.match(admin, /外部サービス連携/);
  assert.match(admin, /接続済み/);
  assert.match(admin, /未接続/);
  // ⚠️ 管理画面に出るのは「ボットが入っている先」。読む先は利用者が選ぶ
  assert.match(admin, /link\.actionUrl/);
});

test("共有相手のアドレスは選んでコピーできる形で出す", () => {
  // 手で写すと打ち間違えて、原因の分からない404になる
  assert.match(admin, /共有相手に追加するアドレス/);
  assert.match(styles, /\.admin-link-share code \{[\s\S]*?user-select: all;/);
});

test("Discordの会話も参照欄に出し、発言へ飛べる", () => {
  // ⚠️ 索引のページではないがリンクは作れる。出さないと「Discordの会話に
  // よれば」と答えながら、どの発言が根拠なのか後から追えない（2026-09-13）
  const chat = readFileSync(new URL("../src/chat.ts", import.meta.url), "utf8");
  const reference = readFileSync(new URL("../src/components/ReferenceSummary.tsx", import.meta.url), "utf8");
  assert.match(chat, /export function referenceItems/);
  assert.match(chat, /source\.origin === "discord"/);
  assert.match(reference, /item\.url/);
  assert.match(reference, /target="_blank"/);
});

test("出典の出所とアシスタントの参照範囲を同じ型にしない", () => {
  // アシスタントは範囲を狭めるもので、索引の資料しか選べない。出典には
  // 設定画面でつないだ共有ドライブやDiscordも出る（2026-09-13のCodex指摘）
  assert.match(api, /export type SourceOrigin = AssistantOrigin \| "drive" \| "discord"/);
  assert.match(api, /origin\?: SourceOrigin;/);
});

test("選んだだけの資料は参照に出さない", () => {
  // 回答が「記載がありません」と言っているのに参照が並ぶ食い違いになる（2026-09-13）
  const chat = readFileSync(new URL("../src/chat.ts", import.meta.url), "utf8");
  assert.match(chat, /source\.used === false/);
  // 古い履歴には used が無い。無ければ当時どおり出す
  assert.match(api, /used\?: boolean;/);
});
