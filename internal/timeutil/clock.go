// Package timeutil 提供伺服器 UTC 時鐘與時間戳存取工具，是全服務業務時間的唯一來源。
//
// 業務時間一律經本套件的 Clock 取得：交易、通知、Phase 與時間線都以伺服器時間為準，
// 不得採信客戶端提交的當前時間（規格 §27.2、SYS-006、ADR-007）。
// 資料庫以 Unix 毫秒整數（UTC）存取，協議層以 RFC 3339 UTC 字串承載（DEC-011），
// 兩者的轉換也只由本套件負責，避免各處各自格式化造成精度或時區不一致。
//
// 逾時、退避與連線期限等「歷時量測」不經本套件：Go 的 time.Duration 與 context 期限
// 已使用作業系統單調讀時，不受牆鐘調整影響；本套件負責的是「某事發生於幾點」的時刻。
package timeutil

import "time"

// Clock 為業務時間來源介面，實作回傳的時刻一律為 UTC。
//
// 服務層與倉儲只依賴本介面而不直接讀牆鐘，因此測試可注入假時鐘，
// 把「發生在某一時刻的業務規則」變成可確定的斷言。
type Clock interface {
	// Now 回傳目前時刻（UTC）。
	Now() time.Time
}

// systemClock 為讀取系統牆鐘的 Clock 實作（無狀態，值型別即可）。
type systemClock struct{}

// System 回傳以系統牆鐘為來源的時鐘。
func System() Clock { return systemClock{} }

// Now 回傳系統牆鐘的 UTC 時刻。
func (systemClock) Now() time.Time { return time.Now().UTC() }

// Test 為可注入的測試時鐘：時刻只在明確操作時改變，不會自行前進。
//
// 未加鎖，僅供單執行緒測試使用；併發測試請各自建立實例。
type Test struct {
	now time.Time
}

// NewTest 以指定時刻建立測試時鐘；非 UTC 的輸入先轉為 UTC 時刻。
func NewTest(at time.Time) *Test { return &Test{now: at.UTC()} }

// Now 回傳目前設定的 UTC 時刻。
func (c *Test) Now() time.Time { return c.now }

// Set 把時鐘設為指定時刻；非 UTC 的輸入先轉為 UTC 時刻。
func (c *Test) Set(at time.Time) { c.now = at.UTC() }

// Advance 把時鐘推進 d；d 為負值即回撥，供過期、倒數與重試類測試使用。
func (c *Test) Advance(d time.Duration) { c.now = c.now.Add(d) }

// 系統時鐘與測試時鐘必須滿足 Clock，介面漂移時在此於編譯期擋下。
var (
	_ Clock = systemClock{}
	_ Clock = (*Test)(nil)
)
