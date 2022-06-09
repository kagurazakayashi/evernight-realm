// 輔助：透過 CDP 取得 headless Chrome 中測試頁的執行結果。
// 用法：先啟動 chrome --headless=new --remote-debugging-port=9222 about:blank，
//       再執行 node cdp.js <目標 URL>。
'use strict';

const http = require('http');

function request(url, method = 'GET') {
  return new Promise((resolve, reject) => {
    const req = http.request(url, { method }, (res) => {
      let data = '';
      res.on('data', (c) => (data += c));
      res.on('end', () => resolve(data));
    });
    req.on('error', reject);
    req.end();
  });
}

async function main() {
  const targetUrl = process.argv[2] || 'http://127.0.0.1:8012/';
  // 新開標籤頁（新版 Chrome 要求 PUT）
  const created = JSON.parse(
    await request(`http://127.0.0.1:9222/json/new?${encodeURIComponent(targetUrl)}`, 'PUT'));
  const ws = new WebSocket(created.webSocketDebuggerUrl);
  let seq = 0;
  const pending = new Map();
  ws.onmessage = (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.id && pending.has(msg.id)) {
      pending.get(msg.id)(msg);
      pending.delete(msg.id);
    }
  };
  const send = (method, params = {}) =>
    new Promise((resolve) => {
      const mid = ++seq;
      pending.set(mid, resolve);
      ws.send(JSON.stringify({ id: mid, method, params }));
    });
  await new Promise((resolve) => (ws.onopen = resolve));
  await send('Runtime.enable');
  await new Promise((r) => setTimeout(r, 2000)); // 等待頁面執行
  const res = await send('Runtime.evaluate', {
    expression: "document.getElementById('out') ? document.getElementById('out').textContent : '(no out node)'",
    returnByValue: true,
  });
  console.log(res.result.result.value);
  process.exit(0);
}

main().catch((e) => {
  console.error('CDP 錯誤:', e.message);
  process.exit(1);
});
