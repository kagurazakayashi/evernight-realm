package timeutil

import (
	"testing"
	"time"
)

// fixed 為測試基準時刻：2026-09-25T12:34:56.789Z（含毫秒，便於同時驗證精度）。
var fixed = time.Date(2026, 9, 25, 12, 34, 56, 789_000_000, time.UTC)

func TestSystemNowIsUTC(t *testing.T) {
	now := System().Now()
	if _, offset := now.Zone(); offset != 0 {
		t.Errorf("System().Now() 應為 UTC，實際區偏移 %d 秒", offset)
	}
	if diff := time.Since(now); diff < -time.Second || diff > time.Second {
		t.Errorf("System().Now() 與系統時間差過大: %v", diff)
	}
	if next := System().Now(); next.Before(now) {
		t.Errorf("後一次取值不得早於前一次：%s 之後是 %s", now, next)
	}
}

func TestTestClockDoesNotAdvanceByItself(t *testing.T) {
	c := NewTest(fixed)
	if got := c.Now(); !got.Equal(fixed) {
		t.Errorf("初始時刻應為 %v，實際 %v", fixed, got)
	}
	// 測試時鐘只在明確操作時改變：兩次取值之間不得自己走。
	if first, second := c.Now(), c.Now(); !first.Equal(second) || !first.Equal(fixed) {
		t.Errorf("未推進時時刻改變了：%v 之後是 %v", first, second)
	}
}

func TestTestClockNormalizesInputToUTC(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("載入測試時區失敗: %v", err)
	}
	// 同一時刻的 +08:00 寫法；存入後一律以 UTC 回傳。
	withOffset := time.Date(2026, 9, 25, 20, 34, 56, 789_000_000, shanghai)

	c := NewTest(withOffset)
	if !c.Now().Equal(fixed) {
		t.Errorf("NewTest 應保留同一時刻，實際 %v", c.Now())
	}
	if _, offset := c.Now().Zone(); offset != 0 {
		t.Errorf("NewTest 應正規化為 UTC，實際區偏移 %d 秒", offset)
	}

	c.Set(withOffset)
	if !c.Now().Equal(fixed) {
		t.Errorf("Set 應保留同一時刻，實際 %v", c.Now())
	}
	if _, offset := c.Now().Zone(); offset != 0 {
		t.Errorf("Set 應正規化為 UTC，實際區偏移 %d 秒", offset)
	}
}

func TestTestClockAdvanceAndSet(t *testing.T) {
	c := NewTest(fixed)

	c.Advance(5 * time.Minute)
	if want := fixed.Add(5 * time.Minute); !c.Now().Equal(want) {
		t.Errorf("前進 5 分鐘後應為 %v，實際 %v", want, c.Now())
	}
	c.Advance(-6 * time.Minute)
	if want := fixed.Add(-time.Minute); !c.Now().Equal(want) {
		t.Errorf("負值應可回撥，期望 %v，實際 %v", want, c.Now())
	}
	c.Set(fixed)
	if !c.Now().Equal(fixed) {
		t.Errorf("Set 後應回到 %v，實際 %v", fixed, c.Now())
	}
}

// stampOf 模擬服務層：只依賴 Clock 介面取得業務時間，不知道背後是哪種實作。
func stampOf(clock Clock) int64 { return ToMillis(clock.Now()) }

func TestBothClocksAreInjectable(t *testing.T) {
	if got, want := stampOf(NewTest(fixed)), fixed.UnixMilli(); got != want {
		t.Errorf("測試時鐘注入後應得到 %d，實際 %d", want, got)
	}
	if got := stampOf(System()); got <= 0 {
		t.Errorf("系統時鐘注入後應得到正數毫秒，實際 %d", got)
	}
}
