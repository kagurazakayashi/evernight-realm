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

// Load 依序以預設值、config.yaml、環境變數建構組態並校驗。
//
// config.yaml 的位置由 opts.ConfigPath 決定；未指定時以
// {opts.DataDir 或預設 evernight-data}/config.yaml 為準。
// 組態檔不存在時以預設值執行（首次啟動時建立範例組態）。
func Load(opts Options) (Config, error) {
	cfg := Default()

	dataDir := opts.DataDir
	if dataDir == "" {
		dataDir = cfg.Server.DataDir
	}

	configPath := opts.ConfigPath
	if configPath == "" {
		configPath = filepath.Join(dataDir, "config.yaml")
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
