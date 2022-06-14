package app

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer 為可跨 goroutine 讀寫的輸出緩衝（run 在背景寫入，測試同步讀取）。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitForListenAddr 等待啟動輸出出現監聽地址並回傳該地址（主機:埠）。
func waitForListenAddr(t *testing.T, out *syncBuffer, runErr <-chan error) string {
	t.Helper()
	const marker = "HTTP 服務已啟動：http://"
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if idx := strings.Index(out.String(), marker); idx >= 0 {
			rest := out.String()[idx+len(marker):]
			if end := strings.IndexAny(rest, " \r\n"); end > 0 {
				return rest[:end]
			}
		}
		select {
		case err := <-runErr:
			t.Fatalf("run 在啟動完成前結束: %v（輸出：%s）", err, out.String())
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("未在期限內看到啟動輸出，目前輸出：%s", out.String())
	return ""
}

func TestRunStopsGracefullyOnContextCancel(t *testing.T) {
	// 以獨立資料目錄與系統指派埠啟動，確認：服務可用 → 取消 context（等同 Ctrl+C）
	// → run 回傳 nil（正常停止）→ 監聽資源已釋放。
	dir := t.TempDir()

	// 先取一個本機可用連接埠後釋放（組態要求明確埠號，不接受 0）。
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("取測試連接埠失敗: %v", err)
	}
	listen := probe.Addr().String()
	_ = probe.Close()
	t.Setenv("ER_SERVER_LISTEN", listen)

	out := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, func() {}, []string{"--data-dir", dir}, out) }()

	addr := waitForListenAddr(t, out, runErr)
	if _, err := net.ResolveTCPAddr("tcp", addr); err != nil {
		t.Fatalf("啟動輸出中的監聽地址無效: %q (%v)", addr, err)
	}

	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("啟動後 /health 請求失敗: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("/health 應回 200 與 status=ok，實際 %d %s", resp.StatusCode, body)
	}

	cancel()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("收到停止請求後 run 應正常結束（回傳 nil），實際: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("收到停止請求後 run 未在期限內結束")
	}

	got := out.String()
	for _, want := range []string{"收到停止信號，開始優雅停止", "服務已停止，監聽資源已釋放。"} {
		if !strings.Contains(got, want) {
			t.Fatalf("停止輸出缺少 %q，實際輸出：%s", want, got)
		}
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("停止後連接埠 %s 應可再次監聽: %v", addr, err)
	}
	_ = ln.Close()
}
