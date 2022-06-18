package timeutil

import (
	"strings"
	"testing"
	"time"
)

func TestToMillisAndFromMillisRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
		want int64
	}{
		{"含毫秒", fixed, 1790339696789},
		{"整秒（毫秒為零）", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 1767225600000},
		{"Unix 起點", time.Unix(0, 0).UTC(), 0},
		{"起點之前（負毫秒）", time.Date(1969, 12, 31, 23, 59, 59, 500_000_000, time.UTC), -500},
		{"亞毫秒部分捨去", time.Date(2026, 9, 25, 12, 34, 56, 789_000_999, time.UTC), 1790339696789},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ToMillis(tc.at)
			if got != tc.want {
				t.Errorf("Unix 毫秒應為 %d，實際 %d", tc.want, got)
			}
			// 讀回後必須是同一毫秒（資料庫以毫秒為精度，往來不得漂移）。
			back := FromMillis(got)
			if FormatUTC(back) != FormatUTC(tc.at) {
				t.Errorf("讀回時刻不符，期望 %v，實際 %v", tc.at, back)
			}
			if ToMillis(back) != got {
				t.Errorf("毫秒往返必須冪等，期望 %d，實際 %d", got, ToMillis(back))
			}
			if _, offset := back.Zone(); offset != 0 {
				t.Errorf("FromMillis 應回傳 UTC 時刻，實際區偏移 %d 秒", offset)
			}
		})
	}
}

func TestFormatUTC(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("載入測試時區失敗: %v", err)
	}
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"UTC 含毫秒", fixed, "2026-09-25T12:34:56.789Z"},
		{"東八區輸入輸出同一瞬間", time.Date(2026, 9, 25, 20, 34, 56, 789_000_000, shanghai), "2026-09-25T12:34:56.789Z"},
		{"整秒仍補零", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "2026-01-01T00:00:00.000Z"},
		{"Unix 起點", time.Unix(0, 0).UTC(), "1970-01-01T00:00:00.000Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatUTC(tc.at); got != tc.want {
				t.Errorf("協議字串應為 %q，實際 %q", tc.want, got)
			}
		})
	}
}

func TestParseUTC(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"Z 結尾", "2026-09-25T12:34:56.789Z", fixed},
		{"無小數秒", "2026-09-25T12:34:56Z", time.Date(2026, 9, 25, 12, 34, 56, 0, time.UTC)},
		{"帶偏移（正規化為 UTC）", "2026-09-25T20:34:56.789+08:00", fixed},
		{"負偏移", "2026-09-25T07:34:56.789-05:00", fixed},
		{"微秒精度（保留原值）", "2026-09-25T12:34:56.789123Z", time.Date(2026, 9, 25, 12, 34, 56, 789123000, time.UTC)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseUTC(tc.input)
			if err != nil {
				t.Fatalf("%q 應可解析: %v", tc.input, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("解析 %q 應得 %v，實際 %v", tc.input, tc.want, got)
			}
			if _, offset := got.Zone(); offset != 0 {
				t.Errorf("回傳值應為 UTC，實際區偏移 %d 秒", offset)
			}
		})
	}
}

func TestParseUTCRejectsAmbiguousOrMalformed(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"缺少時區（無從推定本地時區）", "2026-09-25T12:34:56", "標明時區"},
		{"僅日期", "2026-09-25", "標明時區"},
		{"以空白分隔", "2026-09-25 12:34:56Z", "標明時區"},
		{"月份過界", "2026-13-25T12:34:56Z", "標明時區"},
		{"毫秒整數字串", "1790339696789", "標明時區"},
		{"空字串", "", "標明時區"},
		{"非 ISO 分隔的本地寫法", "25/09/2026 12:34:56 Z", "標明時區"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseUTC(tc.input)
			if err == nil {
				t.Fatalf("應拒絕 %q，實際解析為 %v", tc.input, got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("錯誤訊息應含 %q，實際 %q", tc.want, err)
			}
			if !got.IsZero() {
				t.Errorf("解析失敗時應回傳零值時刻，實際 %v", got)
			}
		})
	}
}

func TestFormatParseRoundTrip(t *testing.T) {
	for _, at := range []time.Time{fixed, time.Unix(0, 0).UTC(), time.Date(2038, 1, 19, 3, 14, 7, 500_000_000, time.UTC)} {
		text := FormatUTC(at)
		back, err := ParseUTC(text)
		if err != nil {
			t.Fatalf("解析自身輸出 %q 失敗: %v", text, err)
		}
		if !back.Equal(at) {
			t.Errorf("往後應得同一時刻，期望 %v，實際 %v", at, back)
		}
		// 資料庫精度為毫秒：以毫秒往返必須完全相等（不受捨入影響）。
		if FromMillis(ToMillis(at)) != back {
			t.Errorf("毫秒往返與字串往返不一致：%v 對 %v", FromMillis(ToMillis(at)), back)
		}
	}
}
