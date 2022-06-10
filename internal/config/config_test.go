package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 寫入臨時組態檔並回傳路徑。
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("寫入測試組態失敗: %v", err)
	}
	return p
}

func TestLoadDefaults(t *testing.T) {
	// 組態檔不存在 → 全部使用內建預設值
	cfg, err := Load(Options{ConfigPath: filepath.Join(t.TempDir(), "nope.yaml")})
	if err != nil {
		t.Fatalf("Load 失敗: %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:5206" {
		t.Errorf("預設 listen 錯誤: %q", cfg.Server.Listen)
	}
	if cfg.Server.DataDir != "evernight-data" {
		t.Errorf("預設 data_dir 錯誤: %q", cfg.Server.DataDir)
	}
	if cfg.Database.BusyTimeoutMS != 5000 {
		t.Errorf("預設 busy_timeout_ms 錯誤: %d", cfg.Database.BusyTimeoutMS)
	}
	if cfg.Security.SessionTTLHours != 24 {
		t.Errorf("預設 session_ttl_hours 錯誤: %d", cfg.Security.SessionTTLHours)
	}
}

func TestLoadYAMLOverrides(t *testing.T) {
	p := writeConfig(t, `
server:
  listen: "0.0.0.0:8090"
  display_timezone: "America/New_York"
database:
  busy_timeout_ms: 3000
logs:
  level: "debug"
`)
	cfg, err := Load(Options{ConfigPath: p})
	if err != nil {
		t.Fatalf("Load 失敗: %v", err)
	}
	if cfg.Server.Listen != "0.0.0.0:8090" {
		t.Errorf("listen 覆蓋失敗: %q", cfg.Server.Listen)
	}
	if cfg.Server.DisplayTimezone != "America/New_York" {
		t.Errorf("時區覆蓋失敗: %q", cfg.Server.DisplayTimezone)
	}
	if cfg.Database.BusyTimeoutMS != 3000 {
		t.Errorf("busy_timeout 覆蓋失敗: %d", cfg.Database.BusyTimeoutMS)
	}
	if cfg.Logs.Level != "debug" {
		t.Errorf("日誌層級覆蓋失敗: %q", cfg.Logs.Level)
	}
	// 未覆蓋欄位保持預設
	if cfg.Database.Path != "evernight.db" {
		t.Errorf("未覆蓋欄位應保持預設: %q", cfg.Database.Path)
	}
}

func TestLoadDataDirDotMeansDefault(t *testing.T) {
	p := writeConfig(t, "server:\n  data_dir: \".\"\n")
	cfg, err := Load(Options{ConfigPath: p})
	if err != nil {
		t.Fatalf("Load 失敗: %v", err)
	}
	if cfg.Server.DataDir != "evernight-data" {
		t.Errorf("data_dir \".\" 應視為預設: %q", cfg.Server.DataDir)
	}
}

func TestLoadEnvOverridesYAML(t *testing.T) {
	p := writeConfig(t, "server:\n  listen: \"127.0.0.1:5206\"\n")
	t.Setenv("ER_SERVER_LISTEN", "127.0.0.1:9090")
	cfg, err := Load(Options{ConfigPath: p})
	if err != nil {
		t.Fatalf("Load 失敗: %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:9090" {
		t.Errorf("環境變數應覆蓋 yaml: %q", cfg.Server.Listen)
	}
}

func TestLoadInvalidEnvInt(t *testing.T) {
	t.Setenv("ER_DATABASE_BUSY_TIMEOUT_MS", "abc")
	_, err := Load(Options{ConfigPath: filepath.Join(t.TempDir(), "none.yaml")})
	if err == nil || !strings.Contains(err.Error(), "ER_DATABASE_BUSY_TIMEOUT_MS") {
		t.Errorf("非法環境變數整數應報錯並指出變數名，實際: %v", err)
	}
}

func TestValidateBadListen(t *testing.T) {
	cases := []struct {
		listen string
		want   string
	}{
		{"", "server.listen 不可為空"},
		{"localhost", "格式無效"},
		{":5206", "缺少主機"},
		{"127.0.0.1:99999", "連接埠無效"},
		{"127.0.0.1:0", "連接埠無效"},
	}
	for _, tc := range cases {
		cfg := Default()
		cfg.Server.Listen = tc.listen
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("listen=%q 應報錯包含 %q，實際: %v", tc.listen, tc.want, err)
		}
	}
}

func TestValidateBadTimezone(t *testing.T) {
	cfg := Default()
	cfg.Server.DisplayTimezone = "Not/AZone"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "display_timezone") {
		t.Errorf("非法時區應指出 display_timezone，實際: %v", err)
	}
}

func TestValidateBadBusyTimeout(t *testing.T) {
	cfg := Default()
	cfg.Database.BusyTimeoutMS = 0
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "busy_timeout_ms") {
		t.Errorf("busy_timeout_ms=0 應報錯指出欄位，實際: %v", err)
	}
}

func TestValidateBadLogLevel(t *testing.T) {
	cfg := Default()
	cfg.Logs.Level = "verbose"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "logs.level") {
		t.Errorf("非法日誌層級應指出 logs.level，實際: %v", err)
	}
}

func TestValidateBadSessionTTL(t *testing.T) {
	cfg := Default()
	cfg.Security.SessionTTLHours = 0
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "session_ttl_hours") {
		t.Errorf("session_ttl_hours=0 應報錯指出欄位，實際: %v", err)
	}
}

func TestValidateRootPasswordHash(t *testing.T) {
	cfg := Default()
	cfg.Security.RootPasswordHash = "plaintext-secret"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "root_password_hash") {
		t.Errorf("非 Argon2id 雜湊應報錯指出欄位，實際: %v", err)
	}

	cfg.Security.RootPasswordHash = "$argon2id$v=19$m=65536,t=2,p=1$c29tZXNhbHQ$hashvalue"
	if err := cfg.Validate(); err != nil {
		t.Errorf("合法 Argon2id 雜湊不應報錯: %v", err)
	}
}

func TestRedactedNeverLeaksSecret(t *testing.T) {
	secret := "$argon2id$v=19$m=65536,t=2,p=1$c29tZXNhbHQ$topsecret-hash"
	cfg := Default()
	cfg.Security.RootPasswordHash = secret
	out := cfg.Redacted()
	if strings.Contains(out, secret) || strings.Contains(out, "topsecret") {
		t.Errorf("Redacted 不得洩漏機密明文: %s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("Redacted 應顯示 [REDACTED] 標記: %s", out)
	}
}

func TestListenAllInterfaces(t *testing.T) {
	cfg := Default()
	cfg.Server.Listen = "0.0.0.0:5206"
	if !cfg.ListenAllInterfaces() {
		t.Error("0.0.0.0 應判定為暴露所有介面")
	}
	cfg.Server.Listen = "127.0.0.1:5206"
	if cfg.ListenAllInterfaces() {
		t.Error("127.0.0.1 不應判定為暴露所有介面")
	}
}

func TestResolvePathsWithSpacesAndNonASCII(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "活動 資料 目录")
	cfg := Default()
	cfg.Server.DataDir = dir
	if err := cfg.Resolve(); err != nil {
		t.Fatalf("Resolve 失敗（空格/非 ASCII 路徑）: %v", err)
	}
	if !filepath.IsAbs(cfg.Database.Path) {
		t.Errorf("database.path 應解析為絕對路徑: %q", cfg.Database.Path)
	}
	if !strings.Contains(cfg.Database.Path, dir) {
		t.Errorf("database.path 應位於資料目錄內: %q", cfg.Database.Path)
	}
	if cfg.Media != filepath.Join(dir, "media") {
		t.Errorf("media 解析錯誤: %q", cfg.Media)
	}
}

func TestResolveKeepsAbsolutePaths(t *testing.T) {
	cfg := Default()
	cfg.Server.DataDir = filepath.Join(t.TempDir(), "data")
	cfg.Database.Path = "D:/external/evernight.db"
	cfg.Media = "/opt/media"
	if err := cfg.Resolve(); err != nil {
		t.Fatalf("Resolve 失敗: %v", err)
	}
	if !filepath.IsAbs(cfg.Database.Path) || !filepath.IsAbs(cfg.Media) {
		t.Error("絕對路徑應原樣保留")
	}
}

func TestResolveRejectsTraversal(t *testing.T) {
	cfg := Default()
	cfg.Server.DataDir = t.TempDir()
	cfg.Media = "../../../escape"
	err := cfg.Resolve()
	if err == nil || !strings.Contains(err.Error(), "路徑穿越拒絕") {
		t.Errorf("相對路徑穿越應被拒絕，實際: %v", err)
	}
}

func TestPrepareCreatesDirsAndExampleConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "evernight-data 測試")
	cfg := Default()
	cfg.Server.DataDir = dir
	if err := cfg.Resolve(); err != nil {
		t.Fatalf("Resolve 失敗: %v", err)
	}
	if err := cfg.Prepare(); err != nil {
		t.Fatalf("Prepare 失敗: %v", err)
	}
	for _, d := range []string{cfg.Media, cfg.Documents, cfg.Attachments, cfg.Backups, cfg.Logs.Dir} {
		fi, err := os.Stat(d)
		if err != nil || !fi.IsDir() {
			t.Errorf("子目錄未建立: %s (%v)", d, err)
		}
	}
	configPath := filepath.Join(dir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("範例組態未建立: %v", err)
	}
	if !strings.Contains(string(data), "listen") {
		t.Error("範例組態內容不完整")
	}

	// 冪等：重複 Prepare 不報錯、不覆蓋已修改的組態
	modified := []byte("# 使用者修改\nserver:\n  listen: \"127.0.0.1:9999\"\n")
	if err := os.WriteFile(configPath, modified, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Prepare(); err != nil {
		t.Fatalf("重複 Prepare 應冪等: %v", err)
	}
	after, _ := os.ReadFile(configPath)
	if string(after) != string(modified) {
		t.Error("重複 Prepare 不應覆蓋使用者組態")
	}
}

func TestParseArgs(t *testing.T) {
	opts, err := ParseArgs([]string{"--data-dir", "D:/my data/er"})
	if err != nil || opts.DataDir != "D:/my data/er" {
		t.Errorf("--data-dir 解析失敗: opts=%+v err=%v", opts, err)
	}
	if _, err := ParseArgs([]string{"--nope"}); err == nil {
		t.Error("未知參數應報錯")
	}
	if _, err := ParseArgs([]string{"extra"}); err == nil {
		t.Error("位置參數應報錯")
	}
}

func TestLoadCommandLineOverridesYAML(t *testing.T) {
	p := writeConfig(t, "server:\n  data_dir: \"yaml-dir\"\n")
	cfg, err := Load(Options{ConfigPath: p, DataDir: "cli-dir"})
	if err != nil {
		t.Fatalf("Load 失敗: %v", err)
	}
	if cfg.Server.DataDir != "cli-dir" {
		t.Errorf("命令列 --data-dir 應優先於 yaml: %q", cfg.Server.DataDir)
	}
}
