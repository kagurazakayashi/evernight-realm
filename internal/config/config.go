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
	Listen          string `yaml:"listen"`           // HTTP 監聽地址，預設 127.0.0.1:3080
	DataDir         string `yaml:"data_dir"`         // 執行資料目錄；"." 或空表示預設 evernight-data
	DisplayTimezone string `yaml:"display_timezone"` // 顯示時區（IANA 名稱）；資料庫仍以 UTC 儲存
}

// DatabaseConfig 為 SQLite 資料庫組態。
type DatabaseConfig struct {
	Path          string `yaml:"path"`            // 資料庫檔名（相對資料目錄）
	BusyTimeoutMS int    `yaml:"busy_timeout_ms"` // 鎖等待超時（毫秒）
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
}

// Default 回傳內建安全預設值（與 config.example.yaml 一致）。
func Default() Config {
	return Config{
		Server: ServerConfig{
			Listen:          "127.0.0.1:3080",
			DataDir:         "evernight-data",
			DisplayTimezone: "Asia/Shanghai",
		},
		Database: DatabaseConfig{
			Path:          "evernight.db",
			BusyTimeoutMS: 5000,
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
		return fmt.Errorf("config: server.listen 格式無效（需 host:port，如 127.0.0.1:3080）: %q", c.Server.Listen)
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

	if c.Database.Path == "" {
		return errors.New("config: database.path 不可為空")
	}
	if c.Database.BusyTimeoutMS < 1 {
		return errors.New("config: database.busy_timeout_ms 必須為正整數（毫秒）")
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
	return nil
}

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
	return fmt.Sprintf("組態摘要：listen=%s data_dir=%s timezone=%s db=%s busy_timeout_ms=%d media=%s documents=%s attachments=%s backups=%s logs_dir=%s logs_level=%s session_ttl_hours=%d root_password_hash=%s",
		c.Server.Listen, c.Server.DataDir, c.Server.DisplayTimezone,
		c.Database.Path, c.Database.BusyTimeoutMS,
		c.Media, c.Documents, c.Attachments, c.Backups,
		c.Logs.Dir, c.Logs.Level,
		c.Security.SessionTTLHours, redact(c.Security.RootPasswordHash))
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
		{&cfg.Media, "ER_MEDIA"},
		{&cfg.Documents, "ER_DOCUMENTS"},
		{&cfg.Attachments, "ER_ATTACHMENTS"},
		{&cfg.Backups, "ER_BACKUPS"},
		{&cfg.Logs.Dir, "ER_LOGS_DIR"},
		{&cfg.Logs.Level, "ER_LOGS_LEVEL"},
		{&cfg.Security.RootPasswordHash, "ER_SECURITY_ROOT_PASSWORD_HASH"},
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
		{&cfg.Security.SessionTTLHours, "ER_SECURITY_SESSION_TTL_HOURS"},
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
	return nil
}

// ExampleYAML 為首次啟動時寫入資料目錄的脫敏範例組態（不含任何真實憑據）。
const ExampleYAML = `# Evernight Realm 服務端組態（首次啟動自動建立）
#
# 本檔案不含真實憑據；root_password_hash 若未設定，系統將於初始化流程產生。
# 修改後重啟服務端生效。

server:
  listen: "127.0.0.1:3080"          # HTTP 監聽地址；區域網部署時改為主機區域網 IP
  data_dir: "."                     # 執行資料目錄（預設 evernight-data/）
  display_timezone: "Asia/Shanghai" # 伺服器顯示時區；資料庫仍以 UTC 儲存

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
