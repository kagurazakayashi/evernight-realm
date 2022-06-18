// 時間戳在資料庫層與協議層的表示轉換（DEC-011）。
//
// 資料庫欄位為 Unix 毫秒整數（INTEGER，一律代表 UTC 時刻），協議欄位為 RFC 3339
// UTC 字串（含毫秒，例：2026-09-25T12:34:56.789Z）。所有落庫與出組的時刻都經本檔
// 函式轉換，不得在業務碼內各自 Format 或各自假定精度。
package timeutil

import (
	"fmt"
	"time"
)

// layoutUTC 為協議輸出格式：固定以 Z 結尾，不輸出本地偏移。
// 保留毫秒位（.000 也照寫），使同一欄位的字串長度與小數點恆定，利於前端對齊與比對。
const layoutUTC = "2006-01-02T15:04:05.000Z"

// ToMillis 把時刻轉成資料庫存取的 Unix 毫秒；亞毫秒部分無條件捨去，
// 因此寫入值與讀回值完全相等（不會因捨入產生兩個時刻）。
func ToMillis(at time.Time) int64 { return at.UnixMilli() }

// FromMillis 把資料庫的 Unix 毫秒還原為 UTC 時刻。
func FromMillis(millis int64) time.Time { return time.UnixMilli(millis).UTC() }

// FormatUTC 輸出協議字串（RFC 3339 UTC、含毫秒）。
func FormatUTC(at time.Time) string { return at.UTC().Format(layoutUTC) }

// ParseUTC 解析 RFC 3339 時間字串並正規化為 UTC 時刻。
//
// 接受 Z 結尾或其他偏移寫法（兩者都是合法的 RFC 3339），回傳值一律為 UTC。
// 刻意不使用 time.ParseInLocation：缺少時區資訊的字串（如 2026-01-01T00:00:00）
// 一律拒絕，因為「未標明時區」在伺服器端無從推定，猜錯會直接寫進帳務與排程。
func ParseUTC(text string) (time.Time, error) {
	at, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}, fmt.Errorf("timeutil: 時間欄位應為 RFC 3339 且標明時區: %w", err)
	}
	return at.UTC(), nil
}
