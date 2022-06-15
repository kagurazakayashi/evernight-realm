// 資料庫檔識別標記與開庫前預檢。
//
// 本服務的資料庫檔在 SQLite 檔頭寫入兩個標記：
//
//   - application_id = 'EVRL'：識別「這是本服務的資料庫檔」，
//     避免把其他程式的 SQLite 檔當成本服務資料庫開啟（實證：驅動層不做任何識別，
//     見 tools/verify/step040-preflight）；
//   - user_version = schema 版本：由遷移器於每支遷移的交易內寫入，與版本表同步推進。
//
// 開庫前先以純檔案讀取（100 位元組）判定，不經 SQLite 引擎、不產生任何寫入；
// 不認識的檔案與較新的 schema 版本在尚未接觸資料庫前即拒絕，
// 使「拒絕」不會改動原檔（規格附錄 E.5：不得在不識別 Schema 版本時繼續寫入）。
//
// 唯讀預檢（PreflightReadOnly）額外以唯讀連線讀取 user_version：
// 主檔檔頭在 WAL 已提交但尚未 checkpoint 時會落後，而連線（含唯讀）看得到最新值，
// 因此唯讀預檢比純檔頭讀取嚴格；唯讀開啟失敗時退回可寫開啟，並由版本表把關。
package database

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// ApplicationID 為本服務資料庫檔的識別碼（檔頭 application_id），即 ASCII 'EVRL'。
const ApplicationID uint32 = 0x4556524C

// SQLite 檔頭格式常數。
const (
	sqliteHeaderMagic   = "SQLite format 3\x00"
	sqliteHeaderSize    = 100
	offsetUserVersion   = 60
	offsetApplicationID = 68
)

// probeTimeout 為唯讀預檢的期限。
const probeTimeout = 5 * time.Second

// 預檢錯誤；呼叫端以 errors.Is 判定類型。
var (
	// ErrNotSQLite 表示檔案不是有效的 SQLite 資料庫檔。
	ErrNotSQLite = errors.New("database: 不是 SQLite 資料庫檔")
	// ErrForeignDatabase 表示檔案是他人的 SQLite 資料庫（application_id 非本服務）。
	ErrForeignDatabase = errors.New("database: 不是本服務的資料庫檔")
	// ErrSchemaTooNew 表示資料庫 schema 版本高於執行檔已知版本。
	ErrSchemaTooNew = errors.New("database: 資料庫 schema 版本高於執行檔")
	// ErrSchemaBehind 表示資料庫 schema 版本落後於執行檔已知版本
	//（交易邊界把關，schema_guard=transaction 時可能出現）。
	ErrSchemaBehind = errors.New("database: 資料庫 schema 版本落後於執行檔")
)

// Preflight 為開庫前預檢模式，嚴格度由低到高。
type Preflight int

const (
	// PreflightHeader 只讀檔頭標記（預設）：零寫入，足以攔截非本服務檔與較新版本。
	PreflightHeader Preflight = iota
	// PreflightReadOnly 另以唯讀連線讀取版本：可攔截檔頭落後（WAL 未 checkpoint）的較新版本。
	PreflightReadOnly
	// PreflightOff 不預檢：直接可寫開啟後再由版本表把關。
	PreflightOff
)

// String 回傳組態值形式的名稱。
func (p Preflight) String() string {
	switch p {
	case PreflightHeader:
		return "header"
	case PreflightReadOnly:
		return "readonly"
	case PreflightOff:
		return "off"
	default:
		return fmt.Sprintf("unknown(%d)", int(p))
	}
}

// ParsePreflight 解析組態值為預檢模式。
func ParsePreflight(value string) (Preflight, error) {
	switch value {
	case "header":
		return PreflightHeader, nil
	case "readonly":
		return PreflightReadOnly, nil
	case "off":
		return PreflightOff, nil
	default:
		return PreflightHeader, fmt.Errorf("database: 不認識的預檢模式 %q（可用 header|readonly|off）", value)
	}
}

// Header 為資料庫檔頭的辨識結果。
type Header struct {
	// Exists 表示檔案存在。
	Exists bool
	// Size 為檔案位元組數。
	Size int64
	// Empty 表示檔案存在但長度為 0（SQLite 會視為新庫）。
	Empty bool
	// SQLite 表示檔頭 magic 相符且長度足夠。
	SQLite bool
	// ApplicationID 為檔頭 application_id（0 表示未標記）。
	ApplicationID uint32
	// SchemaVersion 為檔頭 user_version（0 表示未標記）。
	SchemaVersion int
}

// ReadHeader 以純檔案讀取解析 SQLite 檔頭（不經 SQLite 引擎、不寫入）。
//
// 檔案不存在、長度不足或 magic 不符皆不視為錯誤：結果由 Header 欄位表達，
// 由 checkHeader 決定是否拒絕開庫。
func ReadHeader(path string) (Header, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Header{}, nil
	}
	if err != nil {
		return Header{}, fmt.Errorf("database: 讀取資料庫檔資訊失敗（%s）: %w", path, err)
	}
	header := Header{Exists: true, Size: info.Size()}
	if info.Size() == 0 {
		header.Empty = true
		return header, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return Header{}, fmt.Errorf("database: 開啟資料庫檔讀取檔頭失敗（%s）: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	buf := make([]byte, sqliteHeaderSize)
	if _, err := io.ReadFull(file, buf); err != nil {
		// 長度不足 100 位元組：保留 SQLite=false，交由 checkHeader 拒絕。
		return header, nil
	}
	if string(buf[:len(sqliteHeaderMagic)]) != sqliteHeaderMagic {
		return header, nil
	}
	header.SQLite = true
	header.SchemaVersion = int(binary.BigEndian.Uint32(buf[offsetUserVersion : offsetUserVersion+4]))
	header.ApplicationID = binary.BigEndian.Uint32(buf[offsetApplicationID : offsetApplicationID+4])
	return header, nil
}

// checkHeader 依檔頭判定是否可繼續開庫。
//
// 回傳錯誤時呼叫端必須在尚未對資料庫做任何寫入前中止；
// 檔案不存在或長度為 0 視為新庫，直接放行。
func checkHeader(header Header, knownVersion int) error {
	if !header.Exists || header.Empty {
		return nil
	}
	if !header.SQLite {
		return fmt.Errorf("%w（%d 位元組，檔頭無效或長度不足）；請確認 database.path 指向正確的資料目錄，或改用新的資料目錄",
			ErrNotSQLite, header.Size)
	}
	if header.ApplicationID != 0 && header.ApplicationID != ApplicationID {
		return fmt.Errorf("%w（application_id=0x%08X，本服務為 0x%08X）；為避免破壞其他程式的資料，已拒絕開啟",
			ErrForeignDatabase, header.ApplicationID, ApplicationID)
	}
	if knownVersion > 0 && header.SchemaVersion > knownVersion {
		return fmt.Errorf("%w：資料庫標記版本 %d 高於執行檔已知版本 %d；請改用較新的執行檔，或以備份還原後再啟動",
			ErrSchemaTooNew, header.SchemaVersion, knownVersion)
	}
	return nil
}

// probeSchemaVersion 以唯讀連線讀取 schema 版本（user_version）。
//
// 唯讀連線（mode=ro + query_only）不寫入資料庫，且能看到 WAL 中已提交但尚未
// checkpoint 的檔頭變更，因此比純檔案讀取嚴格。
// WAL 資料庫在需要回復或建立共享記憶體檔時唯讀開啟可能失敗，
// 此時回傳錯誤，由呼叫端決定是否退回可寫開啟（退回後仍由版本表把關）。
func probeSchemaVersion(ctx context.Context, path string, busyTimeout time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	pool, err := sql.Open("sqlite", dsnReadOnly(path, busyTimeout))
	if err != nil {
		return 0, fmt.Errorf("database: 唯讀開啟失敗（%s）: %w", path, err)
	}
	defer func() { _ = pool.Close() }()

	if err := pool.PingContext(ctx); err != nil {
		return 0, fmt.Errorf("database: 唯讀連線失敗（%s）: %w", path, err)
	}
	var version int
	if err := pool.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("database: 唯讀讀取 schema 版本失敗（%s）: %w", path, err)
	}
	return version, nil
}
