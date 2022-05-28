// 驗證瀏覽器 JS 環境（Node 模擬）解析 UUIDv7 與大整數字串（STEP-025 探針）。
//
// 用法：node check.js <樣例.json 路徑>
// 以 BigInt 解析金額字串；以正規式校驗 UUIDv7。
// 示範 Number 在 2^53 起失真，證明金額必須走字串協議。
'use strict';

const fs = require('fs');

const path = process.argv[2] || 'sample.json';
const data = JSON.parse(fs.readFileSync(path, 'utf8'));
let failures = 0;

const expected = {
  max_int64: 9223372036854775807n,
  min_int64: -9223372036854775808n,
  js_safe_max: 9007199254740991n,
  js_safe_max_plus_1: 9007199254740992n,
  hundred_k: 100000n,
  zero: 0n,
};

for (const e of data.amounts) {
  const parsed = BigInt(e.value);
  const ok = parsed === expected[e.name];
  if (!ok) failures++;
  console.log(`[${ok ? 'PASS' : 'FAIL'}] JS BigInt ${e.name} — parsed=${parsed}`);
}

const uuidRe = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
for (const e of data.ids) {
  const ok = uuidRe.test(e.uuid7);
  if (!ok) failures++;
  console.log(`[${ok ? 'PASS' : 'FAIL'}] JS UUIDv7 ${e.name} — ${e.uuid7}`);
}

// 失真示範：Number 無法精確承載 int64 全範圍。
console.log(`[INFO] JS Number 失真示範 — 9223372036854775807 → ${Number('9223372036854775807')}`);

process.exit(failures === 0 ? 0 : 1);
