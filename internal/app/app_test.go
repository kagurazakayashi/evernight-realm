package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kagurazakayashi/evernight-realm/internal/config"
	"github.com/kagurazakayashi/evernight-realm/internal/database"
	"github.com/kagurazakayashi/evernight-realm/internal/database/migrate"
	"github.com/kagurazakayashi/evernight-realm/internal/webassets"
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

// TestRunOwnsDatabaseLockUntilStop 驗證啟動流程實際接上資料庫：
// 啟動後資料庫就緒且鎖由服務持有（第二個寫入者被拒），
// 停止後鎖釋放（資料庫可再次開啟）。
func TestRunOwnsDatabaseLockUntilStop(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "evernight.db")
	ctx := context.Background()

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("取測試連接埠失敗: %v", err)
	}
	listen := probe.Addr().String()
	_ = probe.Close()
	t.Setenv("ER_SERVER_LISTEN", listen)

	out := &syncBuffer{}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- run(runCtx, func() {}, []string{"--data-dir", dir}, out) }()

	waitForListenAddr(t, out, runErr)
	if !strings.Contains(out.String(), "資料庫已就緒") {
		t.Fatalf("啟動輸出應包含資料庫就緒訊息，實際：%s", out.String())
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("啟動後資料庫檔應存在: %v", err)
	}

	// 服務執行中：同一資料庫的第二個寫入者必須被拒絕。
	second, err := database.Open(ctx, database.Options{Path: dbPath, BusyTimeout: time.Second})
	if err == nil {
		_ = second.Close()
		t.Fatal("服務執行中，第二個寫入者應被拒絕")
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("收到停止請求後 run 應正常結束，實際: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("收到停止請求後 run 未在期限內結束")
	}
	if !strings.Contains(out.String(), "資料庫已關閉，單寫入實例鎖已釋放。") {
		t.Fatalf("停止輸出應包含資料庫關閉訊息，實際：%s", out.String())
	}

	// 停止後：鎖已釋放，資料庫可再次開啟。
	after, err := database.Open(ctx, database.Options{Path: dbPath, BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("停止後應可重新開啟資料庫: %v", err)
	}
	if err := after.Close(); err != nil {
		t.Fatalf("關閉資料庫失敗: %v", err)
	}
}

// TestRunAppliesMigrationsBeforeListening 驗證啟動流程會先完成遷移：
// 啟動輸出帶遷移摘要，且資料庫已建立首支遷移的結構。
func TestRunAppliesMigrationsBeforeListening(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "evernight.db")

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("取測試連接埠失敗: %v", err)
	}
	listen := probe.Addr().String()
	_ = probe.Close()
	t.Setenv("ER_SERVER_LISTEN", listen)

	out := &syncBuffer{}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- run(runCtx, func() {}, []string{"--data-dir", dir}, out) }()

	waitForListenAddr(t, out, runErr)
	got := out.String()
	if !strings.Contains(got, "資料庫遷移：已套用") {
		t.Fatalf("首次啟動應回報已套用遷移，實際：%s", got)
	}

	// 啟動輸出順序：資料庫就緒 → 遷移 → 服務已啟動。
	if dbIdx, migIdx, srvIdx := strings.Index(got, "資料庫已就緒"), strings.Index(got, "資料庫遷移："), strings.Index(got, "HTTP 服務已啟動"); !(dbIdx < migIdx && migIdx < srvIdx) {
		t.Fatalf("遷移應在資料庫就緒之後、服務啟動之前完成，實際輸出：%s", got)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("收到停止請求後 run 應正常結束，實際: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("收到停止請求後 run 未在期限內結束")
	}

	db, err := database.Open(context.Background(), database.Options{Path: dbPath, BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("開啟資料庫失敗: %v", err)
	}
	defer func() { _ = db.Close() }()
	if version, err := migrate.Current(context.Background(), db.SQL()); err != nil {
		t.Fatalf("讀取版本失敗: %v", err)
	} else if version == 0 {
		t.Fatal("啟動後資料庫版本不應為 0")
	}
}

// TestMigrateSubcommandAppliesAndIsIdempotent 驗證 migrate 子命令：
// 首次套用遷移、再次執行為冪等且不重複套用。
func TestMigrateSubcommandAppliesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	first := &syncBuffer{}
	if err := Migrate(ctx, []string{"--data-dir", dir}, first); err != nil {
		t.Fatalf("migrate 子命令失敗: %v", err)
	}
	for _, want := range []string{"資料庫遷移：已套用", "資料庫已關閉，單寫入實例鎖已釋放。"} {
		if !strings.Contains(first.String(), want) {
			t.Fatalf("輸出缺少 %q，實際：%s", want, first.String())
		}
	}

	known, err := migrate.Load()
	if err != nil {
		t.Fatalf("讀取內嵌遷移失敗: %v", err)
	}
	db, err := database.Open(ctx, database.Options{Path: filepath.Join(dir, "evernight.db"), BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("開啟資料庫失敗: %v", err)
	}
	version, err := migrate.Current(ctx, db.SQL())
	if err != nil {
		_ = db.Close()
		t.Fatalf("讀取版本失敗: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("關閉資料庫失敗: %v", err)
	}
	if want := known[len(known)-1].Version; version != want {
		t.Fatalf("遷移後版本應為 %d，實際 %d", want, version)
	}

	second := &syncBuffer{}
	if err := Migrate(ctx, []string{"--data-dir", dir}, second); err != nil {
		t.Fatalf("第二次 migrate 子命令失敗: %v", err)
	}
	if !strings.Contains(second.String(), "資料庫遷移：無待套用") {
		t.Fatalf("第二次應回報無待套用，實際：%s", second.String())
	}
}

// TestMigrateDryRunDoesNotChangeDatabase 驗證檢查模式：回報待套用清單但不建立版本表與結構。
func TestMigrateDryRunDoesNotChangeDatabase(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	known, err := migrate.Load()
	if err != nil {
		t.Fatalf("讀取內嵌遷移失敗: %v", err)
	}
	out := &syncBuffer{}
	if err := Migrate(ctx, []string{"--dry-run", "--data-dir", dir}, out); err != nil {
		t.Fatalf("migrate --dry-run 失敗: %v", err)
	}
	got := out.String()
	for _, want := range []string{"資料庫版本檢查（不變更資料）", "目前 version=0", known[0].String()} {
		if !strings.Contains(got, want) {
			t.Fatalf("檢查模式輸出缺少 %q，實際：%s", want, got)
		}
	}

	db, err := database.Open(ctx, database.Options{Path: filepath.Join(dir, "evernight.db"), BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("開啟資料庫失敗: %v", err)
	}
	defer func() { _ = db.Close() }()
	if version, err := migrate.Current(ctx, db.SQL()); err != nil {
		t.Fatalf("讀取版本失敗: %v", err)
	} else if version != 0 {
		t.Fatalf("檢查模式不得變更版本，實際 %d", version)
	}
	var name string
	err = db.SQL().QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", migrate.TableName).Scan(&name)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("檢查模式不應建立版本表，實際查詢結果: %v（%s）", err, name)
	}
}

// TestRunReportsConfiguredTransactionPolicy 驗證交易邊界組態實際接到資料庫並出現在啟動輸出。
func TestRunReportsConfiguredTransactionPolicy(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
database:
  transaction:
    begin_mode: "deferred"
    nested: "savepoint"
    busy_retry_max: 2
    busy_retry_backoff_ms: 25
    timeout_ms: 2500
`), 0o600); err != nil {
		t.Fatalf("寫入組態失敗: %v", err)
	}

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("取測試連接埠失敗: %v", err)
	}
	listen := probe.Addr().String()
	_ = probe.Close()
	t.Setenv("ER_SERVER_LISTEN", listen)

	out := &syncBuffer{}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- run(runCtx, func() {}, []string{"--data-dir", dir}, out) }()

	waitForListenAddr(t, out, runErr)
	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run 失敗: %v（輸出：%s）", err, out.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run 未在期限內結束")
	}

	got := out.String()
	want := "資料庫交易策略：begin_mode=deferred nested=savepoint busy_retry_max=2 " +
		"busy_retry_backoff_ms=25 timeout_ms=2500 schema_guard=startup"
	if !strings.Contains(got, want) {
		t.Fatalf("啟動輸出應含組態指定的交易策略 %q，實際：%s", want, got)
	}
}

// TestTxPolicyFromConfig 驗證組態 → 交易策略的對應，含防禦性錯誤路徑。
func TestTxPolicyFromConfig(t *testing.T) {
	cfg := config.Default()
	policy, err := txPolicy(cfg)
	if err != nil {
		t.Fatalf("預設組態應可建立交易策略: %v", err)
	}
	if policy.BeginMode != database.BeginImmediate || policy.Nested != database.NestedReject ||
		policy.BusyRetryMax != 0 || policy.Timeout != 10*time.Second || policy.GuardSchema {
		t.Fatalf("預設策略不符: %+v", policy)
	}

	cfg.Database.SchemaGuard = "transaction"
	cfg.Database.Transaction.BeginMode = "deferred"
	cfg.Database.Transaction.Nested = "reuse"
	cfg.Database.Transaction.TimeoutMS = 0
	policy, err = txPolicy(cfg)
	if err != nil {
		t.Fatalf("組態應可建立交易策略: %v", err)
	}
	if policy.BeginMode != database.BeginDeferred || policy.Nested != database.NestedReuse ||
		policy.Timeout != 0 || !policy.GuardSchema {
		t.Fatalf("自訂策略不符: %+v", policy)
	}

	// 防禦性檢查：組態校驗清單與 database 的列舉清單漂移時，必須在啟動階段就報錯。
	cfg.Database.Transaction.BeginMode = "exclusive"
	if _, err := txPolicy(cfg); err == nil || !strings.Contains(err.Error(), "database.transaction.begin_mode") {
		t.Fatalf("不認識的 BEGIN 模式應在啟動階段報錯並指出欄位，實際: %v", err)
	}
	cfg.Database.Transaction.BeginMode = "immediate"
	cfg.Database.Transaction.Nested = "merge"
	if _, err := txPolicy(cfg); err == nil || !strings.Contains(err.Error(), "database.transaction.nested") {
		t.Fatalf("不認識的嵌套策略應在啟動階段報錯並指出欄位，實際: %v", err)
	}
}

// TestMigrateRejectedWhileDatabaseInUse 驗證服務（或另一程序）持有單寫入實例鎖時無法執行遷移。
func TestMigrateRejectedWhileDatabaseInUse(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	holder, err := database.Open(ctx, database.Options{Path: filepath.Join(dir, "evernight.db"), BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("開啟資料庫失敗: %v", err)
	}
	defer func() { _ = holder.Close() }()

	err = Migrate(ctx, []string{"--data-dir", dir}, &syncBuffer{})
	if err == nil {
		t.Fatal("資料庫已被持有時 migrate 應失敗")
	}
	if !strings.Contains(err.Error(), "單寫入實例約束") {
		t.Fatalf("錯誤訊息應指出單寫入實例約束，實際: %v", err)
	}
}

// TestEndpointsNoteFollowsWebStatus 驗證啟動行的端點清單與內嵌判定一致。
//
// 列不列「/ 網頁介面」取決於執行檔裡到底有沒有可用的產物：寫了卻回 404，
// 人會先去試那個位址，然後才發現前端根本沒建置。
func TestEndpointsNoteFollowsWebStatus(t *testing.T) {
	available := endpointsNote(webassets.Status{Available: true, Files: 43})
	if !strings.Contains(available, "/ 網頁介面") {
		t.Errorf("可用時應列舉 / ，實際 %q", available)
	}
	for _, want := range []string{"/health 存活", "/ready 就緒", "/time 伺服器時間"} {
		if !strings.Contains(available, want) {
			t.Errorf("端點清單應含 %q，實際 %q", want, available)
		}
	}

	missing := endpointsNote(webassets.Status{Reason: "產物不完整，缺少必要檔案 index.html"})
	if strings.Contains(missing, "/ 網頁介面") {
		t.Errorf("不可用時不應列舉 / ，實際 %q", missing)
	}
	if !strings.Contains(missing, "/health 存活") {
		t.Errorf("不可用時仍要列舉三個基礎端點，實際 %q", missing)
	}
}

// TestRunReportsWebBundle 驗證啟動輸出把內嵌 Web 產物的狀態講清楚，且與判定結果同源。
func TestRunReportsWebBundle(t *testing.T) {
	dir := t.TempDir()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("取測試連接埠失敗: %v", err)
	}
	listen := probe.Addr().String()
	_ = probe.Close()
	t.Setenv("ER_SERVER_LISTEN", listen)

	out := &syncBuffer{}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- run(runCtx, func() {}, []string{"--data-dir", dir}, out) }()

	waitForListenAddr(t, out, runErr)
	_, status := webassets.Dist()

	got := out.String()
	if !strings.Contains(got, "Web 介面："+status.Summary()) {
		t.Fatalf("啟動輸出應原樣帶上內嵌判定，實際：%s", got)
	}
	if !strings.Contains(got, "（"+endpointsNote(status)+"）") {
		t.Fatalf("端點清單應與內嵌判定一致，實際：%s", got)
	}

	// 判定與摘要必須是同一次結論：兩處各查一次就會出現「說有卻沒有」的那一行。
	for _, line := range strings.Split(got, "\n") {
		switch {
		case strings.HasPrefix(line, "Web 介面："):
			if status.Available != strings.Contains(line, "已內嵌") {
				t.Errorf("Web 介面行與判定不一致: %q（可用=%v）", line, status.Available)
			}
		case strings.Contains(line, "HTTP 服務已啟動："):
			if status.Available != strings.Contains(line, "/ 網頁介面") {
				t.Errorf("啟動行與判定不一致: %q（可用=%v）", line, status.Available)
			}
		}
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("收到停止請求後 run 應正常結束，實際: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("收到停止請求後 run 未在期限內結束")
	}
}

// TestRunServesEmbeddedWebWhenAvailable 用真實內嵌產物起服務，驗收「單一封檔能開首頁」。
//
// 產物是否內取決於工作樹裡有沒有跑過前端建置，因此兩種結果都要能被接受：
// 可用時 / 必須回 HTML 外殼，不可用時 / 必須回統一 404 信封——兩者都不該是第三種答案。
func TestRunServesEmbeddedWebWhenAvailable(t *testing.T) {
	dir := t.TempDir()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("取測試連接埠失敗: %v", err)
	}
	listen := probe.Addr().String()
	_ = probe.Close()
	t.Setenv("ER_SERVER_LISTEN", listen)

	out := &syncBuffer{}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- run(runCtx, func() {}, []string{"--data-dir", dir}, out) }()

	addr := waitForListenAddr(t, out, runErr)
	_, status := webassets.Dist()

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET / 失敗: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("讀取回應失敗: %v", err)
	}

	switch {
	case status.Available:
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("內嵌產物可用時 / 應回 200，實際 %d", resp.StatusCode)
		}
		if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Errorf("回應應是 HTML，實際 %q", resp.Header.Get("Content-Type"))
		}
		if !strings.Contains(string(body), "<base href") {
			t.Errorf("回應應是 Flutter Web 外殼，實際 %q", body)
		}
	default:
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("未內嵌產物時 / 應回 404，實際 %d", resp.StatusCode)
		}
		if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			t.Errorf("未內嵌時 / 應回 JSON 信封，實際 %q", resp.Header.Get("Content-Type"))
		}
	}

	// 端點合同不受內嵌影響。
	health, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("GET /health 失敗: %v", err)
	}
	defer health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Errorf("內嵌後 /health 應仍回 200，實際 %d", health.StatusCode)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("收到停止請求後 run 應正常結束，實際: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("收到停止請求後 run 未在期限內結束")
	}
}
