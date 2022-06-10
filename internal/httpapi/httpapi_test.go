package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kagurazakayashi/evernight-realm/internal/config"
)

const testVersion = "0.1.0-test"

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := config.Default()
	srv := New(&cfg, testVersion)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestHealthOK(t *testing.T) {
	ts := testServer(t)
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health 失敗: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("狀態碼應為 200，實際 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type 應為 JSON，實際 %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("回應體應為 JSON: %v (%s)", err, body)
	}
	if got["status"] != "ok" || got["service"] != "evernight-server" || got["version"] != testVersion {
		t.Errorf("存活狀態欄位不符: %v", got)
	}
}

func TestHealthMethodNotAllowed(t *testing.T) {
	ts := testServer(t)
	resp, err := http.Post(ts.URL+"/health", "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST /health 失敗: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("非 GET 應得 405，實際 %d", resp.StatusCode)
	}
}

func TestUnknownPath404(t *testing.T) {
	ts := testServer(t)
	resp, err := http.Get(ts.URL + "/no-such-path")
	if err != nil {
		t.Fatalf("GET /no-such-path 失敗: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知路徑應得 404，實際 %d", resp.StatusCode)
	}
}

func TestListenAndServeWithConfiguredPort(t *testing.T) {
	// 取一個本機可用連接埠後關閉，再以該連接埠啟動服務（驗證地址與連接埠可配置）。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("取測試連接埠失敗: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	cfg := config.Default()
	cfg.Server.Listen = fmt.Sprintf("127.0.0.1:%d", port)
	srv := New(&cfg, testVersion)
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			t.Errorf("ListenAndServe 失敗: %v", err)
		}
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	var resp *http.Response
	for i := 0; i < 50; i++ {
		resp, err = http.Get(url)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("連接埠 %d 上的 /health 請求失敗: %v", port, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("狀態碼應為 200，實際 %d", resp.StatusCode)
	}
}

func TestListenAndServePortInUse(t *testing.T) {
	// 先佔用一個連接埠，再嘗試監聽同一連接埠應回傳錯誤。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("佔用連接埠失敗: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	cfg := config.Default()
	cfg.Server.Listen = fmt.Sprintf("127.0.0.1:%d", port)
	srv := New(&cfg, testVersion)
	if err := srv.ListenAndServe(); err == nil {
		t.Fatal("連接埠被佔用時 ListenAndServe 應回傳錯誤")
	}
}
