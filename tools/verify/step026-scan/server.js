// 驗證 Web 在 HTTP 局域網及 HTTPS 下的掃碼可用條件（探針原型，可丟棄）。
//
// 啟動本機 HTTP 服務（綁定 0.0.0.0），提供測試頁：
//   - 輸出 isSecureContext / navigator.mediaDevices / getUserMedia 存在性
// 用 chromium 無頭模式分別訪問：
//   - http://127.0.0.1:8012  （localhost → secure context）
//   - http://<本機局域網 IP>:8012 （局域網 HTTP → insecure context，RSK-002 根因）
// 不關閉瀏覽器安全（不用 --unsafely-treat-insecure-origin-as-secure）。
//
// 用法：node server.js
'use strict';

const http = require('http');

const page = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>scan-context-test</title></head>
<body><pre id="out">loading...</pre>
<script>
const out = [];
out.push('URL=' + location.href);
out.push('isSecureContext=' + window.isSecureContext);
out.push('mediaDevices=' + (navigator.mediaDevices ? 'available' : 'undefined'));
out.push('getUserMedia=' +
  (navigator.mediaDevices && typeof navigator.mediaDevices.getUserMedia === 'function'
    ? 'function' : 'missing'));
document.getElementById('out').textContent = out.join('\n');
if (navigator.mediaDevices && navigator.mediaDevices.enumerateDevices) {
  navigator.mediaDevices.enumerateDevices().then((devs) => {
    out.push('devices=' + devs.map((d) => d.kind).join(',') || '(none)');
    document.getElementById('out').textContent = out.join('\\n');
  }).catch((e) => {
    out.push('enumerateError=' + e.name);
    document.getElementById('out').textContent = out.join('\\n');
  });
} else {
  document.getElementById('out').textContent = out.join('\\n');
}
</script></body></html>`;

const server = http.createServer((req, res) => {
  res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
  res.end(page);
});
server.listen(8012, '0.0.0.0', () => {
  console.log('scan-context-test listening on :8012');
});
