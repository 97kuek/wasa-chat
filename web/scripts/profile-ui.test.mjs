import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const page = readFileSync(new URL("../src/App.tsx", import.meta.url), "utf8");
const admin = readFileSync(new URL("../src/admin/AdminPage.tsx", import.meta.url), "utf8");
const api = readFileSync(new URL("../src/api.ts", import.meta.url), "utf8");
const styles = readFileSync(new URL("../src/styles.css", import.meta.url), "utf8");

test("本人の利用者画像を設定・変更・解除できる", () => {
  assert.match(page, /accept="image\/png,image\/jpeg,image\/webp"/);
  assert.match(page, /toIconDataURL\(file\)/);
  assert.match(page, /利用者画像を変更/);
  assert.match(page, /利用者画像を外す/);
  assert.match(api, /fetch\(`\$\{API_ORIGIN\}\/api\/profile\/icon`/);
  assert.match(api, /method: "PUT"/);
});

test("通常画面と管理画面に保存した利用者画像を表示する", () => {
  assert.match(page, /<AdminPage username=\{username\} profileIcon=\{profileIcon\}/);
  assert.match(page, /profileIcon\s*\? <img src=\{profileIcon\} alt="" \/>/);
  assert.match(admin, /profileIcon\s*\? <img src=\{profileIcon\} alt="" \/>/);
  assert.match(styles, /\.profile-avatar img \{[\s\S]*?object-fit: cover;/);
});
