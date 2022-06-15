// Package config 提供伺服器組態的載入、預設值與校驗。
//
// 載入順序：內建預設值 → {資料目錄}/config.yaml → 環境變數（ER_ 前綴）。
// 命令列參數（--data-dir）與資料目錄初始化於後續步驟加入。
//
// 校驗失敗時，錯誤訊息會指出具體欄位路徑（如 server.listen），
// 但絕不輸出任何機密明文；啟動日誌一律使用 Redacted() 的脫敏檢視。
package config

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Options 為 Load 的外部輸入。空值表示使用對應的預設/組態值。
type Options struct {
	// DataDir 由命令列或環境變數指定的資料目錄；空字串表示用組態或預設值。
	DataDir string
	// ConfigPath 組態檔路徑；空字串表示 {DataDir 或預設}/config.yaml。
	ConfigPath string
}

// Config 為服務端執行期組態。
type Config struct {
	Server      ServerConfig   `yaml:"server"`
	Database    DatabaseConfig `yaml:"database"`
	Media       string         `yaml:"media"`
	Documents   string         `yaml:"documents"`
	Attachments string         `yaml:"attachments"`
	Backups     string         `yaml:"backups"`
	Logs        LogsConfig     `yaml:"logs"`
	Security    SecurityConfig `yaml:"security"`
}

// ServerConfig 為伺服器層組態。
type ServerConfig struct {
	Listen          string `yaml:"listen"`           // HTTP 監聽地址，預設 127.0.0.1:5206
	DataDir         string `yaml:"data_dir"`         // 執行資料目錄；"." 或空表示預設 evernight-data
	DisplayTimezone string `yaml:"display_timezone"` // 顯示時區（IANA 名稱）；資料庫仍以 UTC 儲存

	// HTTP 層保護參數（STEP-034）：逾時單位為毫秒，請求體上限單位為位元組。
	// 逾時分工：read_header 限制標頭讀取、read 限制整個請求讀取、
	// write 限制回應寫入、request 為單次處理的處理器期限、idle 限制 keep-alive 空閒。
	ReadHeaderTimeoutMS int   `yaml:"read_header_timeout_ms"`
	ReadTimeoutMS       int   `yaml:"read_timeout_ms"`
	WriteTimeoutMS      int   `yaml:"write_timeout_ms"`
	IdleTimeoutMS       int   `yaml:"idle_timeout_ms"`
	RequestTimeoutMS    int   `yaml:"request_timeout_ms"`
	MaxBodyBytes        int64 `yaml:"max_body_bytes"` // JSON API 請求體上限；上傳路由日後單獨放寬

	// ShutdownTimeoutMS 為優雅停止時等待進行中請求完成的上限（STEP-036）。
	// 逾時則強制關閉連線，確保程序能結束並釋放監聽資源。
	ShutdownTimeoutMS int `yaml:"shutdown_timeout_ms"`
}

// DatabaseConfig 為 SQLite 資料庫組態。
type DatabaseConfig struct {
	Path          string `yaml:"path"`            // 資料庫檔名（相對資料目錄）
	BusyTimeoutMS int    `yaml:"busy_timeout_ms"` // 鎖等待超時（毫秒）
	// Preflight 為開庫前預檢模式（STEP-040）：
	// header（預設）只讀檔頭標記，零寫入即可攔截非本服務檔與較新版本；
	// readonly 另以唯讀連線讀取版本（可攔截檔頭落後的較新版本）；off 不預檢。
	Preflight string `yaml:"preflight"`
	// IntegrityCheck 為啟動時是否執行完整性自檢（integrity_check + foreign_key_check）。
	// 預設關閉：大庫上自檢可能明顯耗時，需要時再開，或手動執行 `migrate --verify`。
	IntegrityCheck bool `yaml:"integrity_check"`
	// SchemaGuard 為不識別 schema 版本時的寫入把關層級（規格附錄 E.5）：
	// startup（預設）於啟動時拒絕不相容版本；
	// transaction 另於每個寫入交易的回呼執行前複驗版本（見 database.TxPolicy.GuardSchema），
	// 可攔截執行期資料庫被替換或還原成其他版本。
	SchemaGuard string `yaml:"schema_guard"`
	// Transaction 為交易邊界行為（STEP-041）。
	Transaction TransactionConfig `yaml:"transaction"`
}

// TransactionConfig 為交易邊界行為（STEP-041）。
//
// 回呼式交易由 database.InTx 提供：回呼回傳錯誤或 panic 時整體回滾。
type TransactionConfig struct {
	// BeginMode 為寫入交易的 BEGIN 模式：
	// immediate（預設）在交易開始即取得寫入鎖，失敗點固定於交易開始前；
	// deferred 則延到首次寫入才取得，失敗點落在回呼執行途中（不建議）。
	BeginMode string `yaml:"begin_mode"`
	// Nested 為同一交易內再開交易的行為：
	// reject（預設）直接拒絕；savepoint 以儲存點實現真嵌套（內層可獨立回滾）；
	// reuse 加入外層交易（內層失敗需由外層決定是否整體回滾）。
	Nested string `yaml:"nested"`
	// BusyRetryMax 為遇到忙鎖（SQLITE_BUSY/LOCKED）時自動重試整個交易的次數；
	// 0（預設）不重試。重試會重跑回呼，故僅適用於幂等交易。
	BusyRetryMax int `yaml:"busy_retry_max"`
	// BusyRetryBackoffMS 為重試的線性退避基數（毫秒）；0 表示立即重試。
	BusyRetryBackoffMS int `yaml:"busy_retry_backoff_ms"`
	// TimeoutMS 為單一交易的執行期限（毫秒）；0 表示不限制。
	// 短交易可避免長期持有寫入鎖（規格 RSK-003）。
	TimeoutMS int `yaml:"timeout_ms"`
}

// LogsConfig 為本地日誌組態。
type LogsConfig struct {
	Dir   string `yaml:"dir"`
	Level string `yaml:"level"` // debug | info | warn | error
}

// SecurityConfig 為安全相關組態。
type SecurityConfig struct {
	SessionTTLHours int `yaml:"session_ttl_hours"`
	// Root 憑據（Argon2id 雜湊）僅存於資料目錄 config.yaml；
	// Redacted() 與所有日誌永不輸出其明文。
	RootPasswordHash string `yaml:"root_password_hash"`
	// Headers 為 HTTP 安全回應頭（STEP-035）。
	Headers SecurityHeadersConfig `yaml:"headers"`
}

// SecurityHeadersConfig 為 HTTP 安全回應頭組態。
//
// 空值代表採用內建預設策略（見 httpapi 套件的預設常數）；只有確實需要時才覆寫。
// 覆寫內容由部署者自行負責，服務端僅校驗基本健全性，阻擋明顯放寬（如 'unsafe-eval'）。
type SecurityHeadersConfig struct {
	// ContentSecurityPolicy 為完整 CSP 策略字串；空值表示使用內建預設（相容本機 Flutter Web）。
	ContentSecurityPolicy string `yaml:"content_security_policy"`
	// FrameOptions 為 X-Frame-Options；空值表示 DENY，可選 DENY 或 SAMEORIGIN。
	FrameOptions string `yaml:"frame_options"`
	// ReferrerPolicy 為 Referrer-Policy；空值表示內建預設。
	ReferrerPolicy string `yaml:"referrer_policy"`
	// PermissionsPolicy 為 Permissions-Policy；空值表示內建預設（相機僅允許同源）。
	PermissionsPolicy string `yaml:"permissions_policy"`
}

// Default 回傳內建安全預設值（與 config.example.yaml 一致）。
func Default() Config {
	return Config{
		Server: ServerConfig{
			Listen:              "127.0.0.1:5206",
			DataDir:             "evernight-data",
			DisplayTimezone:     "Asia/Shanghai",
			ReadHeaderTimeoutMS: 5000,
			ReadTimeoutMS:       15000,
			WriteTimeoutMS:      30000,
			IdleTimeoutMS:       60000,
			RequestTimeoutMS:    10000,
			MaxBodyBytes:        1 << 20,
			ShutdownTimeoutMS:   10000,
		},
		Database: DatabaseConfig{
			Path:          "evernight.db",
			BusyTimeoutMS: 5000,
			Preflight:     "header",
			SchemaGuard:   "startup",
			Transaction: TransactionConfig{
				BeginMode:          "immediate",
				Nested:             "reject",
				BusyRetryMax:       0,
				BusyRetryBackoffMS: 50,
				TimeoutMS:          10000,
			},
		},
		Media:       "media/",
		Documents:   "documents/",
		Attachments: "attachments/",
		Backups:     "backups/",
		Logs: LogsConfig{
			Dir:   "logs/",
			Level: "info",
		},
		Security: SecurityConfig{
			SessionTTLHours: 24,
		},
	}
}

// ParseArgs 解析命令列參數，目前支援 --data-dir；未知參數回傳錯誤。
func ParseArgs(args []string) (Options, error) {
	var opts Options
	fs := flag.NewFlagSet("evernight-server", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.DataDir, "data-dir", "", "執行資料目錄（預設 evernight-data/）")
	if err := fs.Parse(args); err != nil {
		return Options{}, fmt.Errorf("命令列參數錯誤: %w", err)
	}
	if fs.NArg() > 0 {
		return Options{}, fmt.Errorf("命令列參數錯誤: 不認識的位置參數 %q", fs.Arg(0))
	}
	return opts, nil
}

// Load 依序以預設值、config.yaml、環境變數、命令列建構組態並校驗。
//
// config.yaml 的位置由 opts.ConfigPath 決定；未指定時以
// {opts.DataDir 或預設 evernight-data}/config.yaml 為準。
// 組態檔不存在時以預設值執行（首次啟動時建立範例組態）。
// 優先序：命令列 --data-dir > 環境變數 > config.yaml > 預設值。
func Load(opts Options) (Config, error) {
	cfg := Default()

	// 組態檔位置由外部指定或預設資料目錄決定；
	// yaml 內部的 data_dir 不影響組態檔讀取位置（避免循環依賴）。
	configDir := opts.DataDir
	if configDir == "" {
		configDir = Default().Server.DataDir
	}

	configPath := opts.ConfigPath
	if configPath == "" {
		configPath = filepath.Join(configDir, "config.yaml")
	}

	if data, err := os.ReadFile(configPath); err == nil {
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("config: 解析 %s 失敗（請檢查 YAML 語法）: %w", configPath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("config: 讀取 %s 失敗: %w", configPath, err)
	}

	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}

	// 命令列 --data-dir 優先於環境變數與 yaml。
	if opts.DataDir != "" {
		cfg.Server.DataDir = opts.DataDir
	}

	// "." 或空 data_dir 表示採用預設資料目錄（與 config.example.yaml 語意一致）。
	if cfg.Server.DataDir == "" || cfg.Server.DataDir == "." {
		cfg.Server.DataDir = Default().Server.DataDir
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate 校驗組態值，錯誤訊息包含欄位路徑（如 server.listen）。
func (c *Config) Validate() error {
	if c.Server.Listen == "" {
		return errors.New("config: server.listen 不可為空")
	}
	host, port, err := net.SplitHostPort(c.Server.Listen)
	if err != nil {
		return fmt.Errorf("config: server.listen 格式無效（需 host:port，如 127.0.0.1:5206）: %q", c.Server.Listen)
	}
	if host == "" {
		return errors.New("config: server.listen 缺少主機（禁止裸埠監聽）")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("config: server.listen 連接埠無效（需 1–65535）: %q", port)
	}

	if c.Server.DataDir == "" {
		return errors.New("config: server.data_dir 不可為空")
	}
	if _, err := time.LoadLocation(c.Server.DisplayTimezone); err != nil {
		return fmt.Errorf("config: server.display_timezone 不是有效 IANA 時區: %q", c.Server.DisplayTimezone)
	}

	// HTTP 層保護參數：全部需為正數；處理期限不得長於回應寫入期限，
	// 否則連線期限會先觸發，用戶端得到連線中斷而非穩定錯誤碼。
	timeouts := []struct {
		name  string
		value int
	}{
		{"server.read_header_timeout_ms", c.Server.ReadHeaderTimeoutMS},
		{"server.read_timeout_ms", c.Server.ReadTimeoutMS},
		{"server.write_timeout_ms", c.Server.WriteTimeoutMS},
		{"server.idle_timeout_ms", c.Server.IdleTimeoutMS},
		{"server.request_timeout_ms", c.Server.RequestTimeoutMS},
		{"server.shutdown_timeout_ms", c.Server.ShutdownTimeoutMS},
	}
	for _, t := range timeouts {
		if t.value < 1 {
			return fmt.Errorf("config: %s 必須為正整數（毫秒）", t.name)
		}
	}
	if c.Server.RequestTimeoutMS > c.Server.WriteTimeoutMS {
		return fmt.Errorf("config: server.request_timeout_ms（%d）不得大於 server.write_timeout_ms（%d）",
			c.Server.RequestTimeoutMS, c.Server.WriteTimeoutMS)
	}
	if c.Server.MaxBodyBytes < minBodyBytes || c.Server.MaxBodyBytes > maxBodyBytes {
		return fmt.Errorf("config: server.max_body_bytes 需介於 %d 與 %d 之間（位元組），實際為 %d",
			minBodyBytes, maxBodyBytes, c.Server.MaxBodyBytes)
	}

	if c.Database.Path == "" {
		return errors.New("config: database.path 不可為空")
	}
	if c.Database.BusyTimeoutMS < 1 {
		return errors.New("config: database.busy_timeout_ms 必須為正整數（毫秒）")
	}
	// 預檢模式與寫入把關層級限枚舉值；空值代表採用內建預設（與 Default() 一致）。
	if c.Database.Preflight == "" {
		c.Database.Preflight = "header"
	}
	switch c.Database.Preflight {
	case "header", "readonly", "off":
	default:
		return fmt.Errorf("config: database.preflight 需為 header|readonly|off，實際為 %q", c.Database.Preflight)
	}
	if c.Database.SchemaGuard == "" {
		c.Database.SchemaGuard = "startup"
	}
	switch c.Database.SchemaGuard {
	case "startup", "transaction":
	default:
		return fmt.Errorf("config: database.schema_guard 需為 startup|transaction，實際為 %q", c.Database.SchemaGuard)
	}
	// 交易邊界（STEP-041）：空值代表採用內建預設（與 Default() 一致）。
	tx := &c.Database.Transaction
	if tx.BeginMode == "" {
		tx.BeginMode = "immediate"
	}
	switch tx.BeginMode {
	case "immediate", "deferred":
	default:
		return fmt.Errorf("config: database.transaction.begin_mode 需為 immediate|deferred，實際為 %q", tx.BeginMode)
	}
	if tx.Nested == "" {
		tx.Nested = "reject"
	}
	switch tx.Nested {
	case "reject", "savepoint", "reuse":
	default:
		return fmt.Errorf("config: database.transaction.nested 需為 reject|savepoint|reuse，實際為 %q", tx.Nested)
	}
	if tx.BusyRetryMax < 0 {
		return fmt.Errorf("config: database.transaction.busy_retry_max 不可為負數，實際為 %d", tx.BusyRetryMax)
	}
	if tx.BusyRetryBackoffMS < 0 {
		return fmt.Errorf("config: database.transaction.busy_retry_backoff_ms 不可為負數，實際為 %d", tx.BusyRetryBackoffMS)
	}
	if tx.TimeoutMS < 0 {
		return fmt.Errorf("config: database.transaction.timeout_ms 不可為負數（0 表示不限制），實際為 %d", tx.TimeoutMS)
	}

	for name, v := range map[string]string{
		"media":       c.Media,
		"documents":   c.Documents,
		"attachments": c.Attachments,
		"backups":     c.Backups,
	} {
		if v == "" {
			return fmt.Errorf("config: %s 目錄不可為空", name)
		}
	}
	if c.Logs.Dir == "" {
		return errors.New("config: logs.dir 不可為空")
	}
	switch c.Logs.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: logs.level 需為 debug|info|warn|error，實際為 %q", c.Logs.Level)
	}

	if c.Security.SessionTTLHours < 1 {
		return errors.New("config: security.session_ttl_hours 必須為正整數")
	}
	if c.Security.RootPasswordHash != "" &&
		!strings.HasPrefix(c.Security.RootPasswordHash, "$argon2id$") {
		return errors.New("config: security.root_password_hash 需為 Argon2id 雜湊（$argon2id$ 前綴）")
	}

	// 安全回應頭：Frame 限制正規化為大寫並限枚舉值；
	// CSP 覆寫必須是有效策略且不得引入 'unsafe-eval'（專案安全基線）。
	c.Security.Headers.FrameOptions = strings.ToUpper(strings.TrimSpace(c.Security.Headers.FrameOptions))
	switch c.Security.Headers.FrameOptions {
	case "", "DENY", "SAMEORIGIN":
	default:
		return fmt.Errorf("config: security.headers.frame_options 需為 DENY 或 SAMEORIGIN，實際為 %q",
			c.Security.Headers.FrameOptions)
	}
	if csp := strings.TrimSpace(c.Security.Headers.ContentSecurityPolicy); csp != "" {
		if !strings.Contains(csp, "default-src") && !strings.Contains(csp, "script-src") {
			return errors.New("config: security.headers.content_security_policy 需至少包含 default-src 或 script-src 指令")
		}
		if strings.Contains(csp, "'unsafe-eval'") {
			return errors.New("config: security.headers.content_security_policy 不得包含 'unsafe-eval'（專案安全基線）")
		}
	}
	return nil
}

// 請求體上限的合理區間：過小會使正常 JSON 請求失效，過大則失去保護意義。
const (
	minBodyBytes int64 = 1 << 10  // 1 KiB
	maxBodyBytes int64 = 64 << 20 // 64 MiB
)

// ListenAllInterfaces 回傳 true 表示監聽地址暴露於所有介面（含公網網卡），
// 供啟動日誌輸出風險提示。
func (c Config) ListenAllInterfaces() bool {
	host, _, err := net.SplitHostPort(c.Server.Listen)
	if err != nil {
		return false
	}
	return host == "0.0.0.0" || host == "::" || host == "[::]"
}

// Redacted 回傳組態的脫敏摘要（供啟動日誌），機密欄位一律顯示 [REDACTED]。
func (c Config) Redacted() string {
	return fmt.Sprintf("組態摘要：listen=%s data_dir=%s timezone=%s db=%s busy_timeout_ms=%d media=%s documents=%s attachments=%s backups=%s logs_dir=%s logs_level=%s session_ttl_hours=%d root_password_hash=%s http_timeouts_ms=[read_header=%d read=%d write=%d idle=%d request=%d shutdown=%d] max_body_bytes=%d security_headers=[frame_options=%s csp=%s referrer_policy=%s permissions_policy=%s] tx=[begin_mode=%s nested=%s busy_retry_max=%d busy_retry_backoff_ms=%d timeout_ms=%d schema_guard=%s]",
		c.Server.Listen, c.Server.DataDir, c.Server.DisplayTimezone,
		c.Database.Path, c.Database.BusyTimeoutMS,
		c.Media, c.Documents, c.Attachments, c.Backups,
		c.Logs.Dir, c.Logs.Level,
		c.Security.SessionTTLHours, redact(c.Security.RootPasswordHash),
		c.Server.ReadHeaderTimeoutMS, c.Server.ReadTimeoutMS, c.Server.WriteTimeoutMS,
		c.Server.IdleTimeoutMS, c.Server.RequestTimeoutMS, c.Server.ShutdownTimeoutMS, c.Server.MaxBodyBytes,
		orDefault(c.Security.Headers.FrameOptions, "DENY"),
		presence(c.Security.Headers.ContentSecurityPolicy),
		presence(c.Security.Headers.ReferrerPolicy),
		presence(c.Security.Headers.PermissionsPolicy),
		c.Database.Transaction.BeginMode, c.Database.Transaction.Nested,
		c.Database.Transaction.BusyRetryMax, c.Database.Transaction.BusyRetryBackoffMS,
		c.Database.Transaction.TimeoutMS, c.Database.SchemaGuard)
}

// presence 將安全回應頭的覆寫值表示為「自訂」或「(預設)」，避免摘要輸出整段策略。
func presence(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(預設)"
	}
	return "自訂"
}

// orDefault 回傳非空值，空值時回傳預設表示。
func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func redact(s string) string {
	if s == "" {
		return "(未設定)"
	}
	return "[REDACTED]"
}

// 環境變數覆蓋規則：ER_ 前綴 + 全大寫欄位名（如 ER_SERVER_LISTEN）。
// 環境變數優先於 config.yaml。

func applyEnv(cfg *Config) error {
	type strField struct {
		dst *string
		env string
	}
	strs := []strField{
		{&cfg.Server.Listen, "ER_SERVER_LISTEN"},
		{&cfg.Server.DataDir, "ER_SERVER_DATA_DIR"},
		{&cfg.Server.DisplayTimezone, "ER_SERVER_DISPLAY_TIMEZONE"},
		{&cfg.Database.Path, "ER_DATABASE_PATH"},
		{&cfg.Database.Transaction.BeginMode, "ER_DATABASE_TRANSACTION_BEGIN_MODE"},
		{&cfg.Database.Transaction.Nested, "ER_DATABASE_TRANSACTION_NESTED"},
		{&cfg.Media, "ER_MEDIA"},
		{&cfg.Documents, "ER_DOCUMENTS"},
		{&cfg.Attachments, "ER_ATTACHMENTS"},
		{&cfg.Backups, "ER_BACKUPS"},
		{&cfg.Logs.Dir, "ER_LOGS_DIR"},
		{&cfg.Logs.Level, "ER_LOGS_LEVEL"},
		{&cfg.Security.RootPasswordHash, "ER_SECURITY_ROOT_PASSWORD_HASH"},
		{&cfg.Security.Headers.ContentSecurityPolicy, "ER_SECURITY_HEADERS_CONTENT_SECURITY_POLICY"},
		{&cfg.Security.Headers.FrameOptions, "ER_SECURITY_HEADERS_FRAME_OPTIONS"},
		{&cfg.Security.Headers.ReferrerPolicy, "ER_SECURITY_HEADERS_REFERRER_POLICY"},
		{&cfg.Security.Headers.PermissionsPolicy, "ER_SECURITY_HEADERS_PERMISSIONS_POLICY"},
	}
	for _, f := range strs {
		if v := os.Getenv(f.env); v != "" {
			*f.dst = v
		}
	}

	type intField struct {
		dst *int
		env string
	}
	ints := []intField{
		{&cfg.Database.BusyTimeoutMS, "ER_DATABASE_BUSY_TIMEOUT_MS"},
		{&cfg.Database.Transaction.BusyRetryMax, "ER_DATABASE_TRANSACTION_BUSY_RETRY_MAX"},
		{&cfg.Database.Transaction.BusyRetryBackoffMS, "ER_DATABASE_TRANSACTION_BUSY_RETRY_BACKOFF_MS"},
		{&cfg.Database.Transaction.TimeoutMS, "ER_DATABASE_TRANSACTION_TIMEOUT_MS"},
		{&cfg.Security.SessionTTLHours, "ER_SECURITY_SESSION_TTL_HOURS"},
		{&cfg.Server.ReadHeaderTimeoutMS, "ER_SERVER_READ_HEADER_TIMEOUT_MS"},
		{&cfg.Server.ReadTimeoutMS, "ER_SERVER_READ_TIMEOUT_MS"},
		{&cfg.Server.WriteTimeoutMS, "ER_SERVER_WRITE_TIMEOUT_MS"},
		{&cfg.Server.IdleTimeoutMS, "ER_SERVER_IDLE_TIMEOUT_MS"},
		{&cfg.Server.RequestTimeoutMS, "ER_SERVER_REQUEST_TIMEOUT_MS"},
		{&cfg.Server.ShutdownTimeoutMS, "ER_SERVER_SHUTDOWN_TIMEOUT_MS"},
	}
	for _, f := range ints {
		if v := os.Getenv(f.env); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("config: 環境變數 %s 需為整數，實際為 %q", f.env, v)
			}
			*f.dst = n
		}
	}

	// 請求體上限為 int64，單獨解析以免在 32 位元平台溢位。
	if v := os.Getenv("ER_SERVER_MAX_BODY_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("config: 環境變數 ER_SERVER_MAX_BODY_BYTES 需為整數，實際為 %q", v)
		}
		cfg.Server.MaxBodyBytes = n
	}
	return nil
}

// ExampleYAML 為首次啟動時寫入資料目錄的脫敏範例組態（不含任何真實憑據）。
const ExampleYAML = `# Evernight Realm 服務端組態（首次啟動自動建立）
#
# 本檔案不含真實憑據；root_password_hash 若未設定，系統將於初始化流程產生。
# 修改後重啟服務端生效。

server:
  listen: "127.0.0.1:5206"          # HTTP 監聽地址；區域網部署時改為主機區域網 IP
  data_dir: "."                     # 執行資料目錄（預設 evernight-data/）
  display_timezone: "Asia/Shanghai" # 伺服器顯示時區；資料庫仍以 UTC 儲存

  # HTTP 層保護參數（單位：毫秒 / 位元組）；request 不得大於 write。
  read_header_timeout_ms: 5000      # 標頭讀取逾時
  read_timeout_ms: 15000            # 整個請求讀取逾時
  write_timeout_ms: 30000           # 回應寫入逾時
  idle_timeout_ms: 60000            # keep-alive 空閒逾時
  request_timeout_ms: 10000         # 單次處理期限（超過回 503）
  max_body_bytes: 1048576           # JSON 請求體上限（1 MiB）

database:
  path: "evernight.db"              # SQLite 檔案（相對 data_dir）
  busy_timeout_ms: 5000              # 鎖等待超時

media: "media/"                     # 媒體檔案
# documents / attachments / backups 與 media 同理，省略時沿用預設。

logs:
  dir: "logs/"
  level: "info"                     # debug | info | warn | error

security:
  session_ttl_hours: 24

  # HTTP 安全回應頭（STEP-035）；留空即用內建預設，只有確實需要時才覆寫。
  # frame_options 可選 DENY | SAMEORIGIN；csp 不得含 'unsafe-eval'。
  headers:
    content_security_policy: ""       # 空 = 內建預設（相容本機 Flutter Web）
    frame_options: ""                 # 空 = DENY
    referrer_policy: ""               # 空 = no-referrer
    permissions_policy: ""            # 空 = camera=(self), microphone=(), geolocation=()
`

// Resolve 將所有相對路徑欄位解析為資料目錄下的絕對路徑並清除冗餘片段。
// 相對路徑不得逃出資料目錄（拒絕路徑穿越）；絕對路徑原樣保留。
// 含空格與非 ASCII 字元的路徑由 Go 字串原生支援，不需額外處理。
func (c *Config) Resolve() error {
	absDir, err := filepath.Abs(c.Server.DataDir)
	if err != nil {
		return fmt.Errorf("config: 解析資料目錄 %q 失敗: %w", c.Server.DataDir, err)
	}
	dataDir := filepath.Clean(absDir)
	c.Server.DataDir = dataDir

	type field struct {
		dst  *string
		name string
	}
	fields := []field{
		{&c.Database.Path, "database.path"},
		{&c.Media, "media"},
		{&c.Documents, "documents"},
		{&c.Attachments, "attachments"},
		{&c.Backups, "backups"},
		{&c.Logs.Dir, "logs.dir"},
	}
	for _, f := range fields {
		resolved, err := joinWithin(dataDir, *f.dst)
		if err != nil {
			return fmt.Errorf("config: %s %v", f.name, err)
		}
		*f.dst = resolved
	}
	return nil
}

// joinWithin 將相對路徑解析於 base 內；絕對路徑保留。
func joinWithin(base, p string) (string, error) {
	if p == "" {
		return "", errors.New("不可為空")
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	resolved := filepath.Clean(filepath.Join(base, p))
	rel, err := filepath.Rel(base, resolved)
	if err != nil {
		return "", fmt.Errorf("解析失敗: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("路徑穿越拒絕: %q", p)
	}
	return resolved, nil
}

// Prepare 建立資料目錄與子目錄、探測可寫性，並在缺少組態檔時寫入範例組態。
// 重複執行（重複啟動）為冪等操作。
func (c *Config) Prepare() error {
	dirs := []string{
		c.Server.DataDir,
		c.Media,
		c.Documents,
		c.Attachments,
		c.Backups,
		c.Logs.Dir,
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return fmt.Errorf("config: 建立目錄 %s 失敗: %w", d, err)
		}
	}

	// 可寫性探測：寫入後立即刪除，確保資料目錄實際可寫（SYS-010）。
	probe := filepath.Join(c.Server.DataDir, ".write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return fmt.Errorf("config: 資料目錄 %s 不可寫: %w", c.Server.DataDir, err)
	}
	if err := os.Remove(probe); err != nil {
		return fmt.Errorf("config: 資料目錄清理失敗: %w", err)
	}

	configPath := filepath.Join(c.Server.DataDir, "config.yaml")
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(configPath, []byte(ExampleYAML), 0o600); err != nil {
			return fmt.Errorf("config: 建立範例組態 %s 失敗: %w", configPath, err)
		}
	}
	return nil
}
