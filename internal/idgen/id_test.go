package idgen

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// canonicalV7 為協議要求的完整格式：36 字元小寫正規字串、版本欄位為 7、變體為 RFC 4122。
var canonicalV7 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// sampleV7 為一個格式正規的 UUIDv7 樣本（值固定，僅供解析測試使用）。
const sampleV7 = "018f6b3a-2c1d-7e4f-8a9b-0c1d2e3f4a5b"

func TestNewMatchesProtocolFormat(t *testing.T) {
	for range 200 {
		id, err := New()
		if err != nil {
			t.Fatalf("New 失敗: %v", err)
		}
		s := id.String()
		if !canonicalV7.MatchString(s) {
			t.Fatalf("產生的 %q 不符合協議格式（36 字元小寫正規 UUIDv7）", s)
		}
		if id.IsNil() {
			t.Fatal("New 不得回傳未指派的零值標識")
		}
		back, err := Parse(s)
		if err != nil {
			t.Fatalf("自身產生的字串應可解析回來: %q: %v", s, err)
		}
		if back != id {
			t.Fatalf("往返回值不相等，期望 %v，實際 %v", id, back)
		}
	}
}

func TestNewOrderPrefixStrictlyIncreases(t *testing.T) {
	const count = 20000
	ids := make([]ID, count)
	for i := range ids {
		id, err := New()
		if err != nil {
			t.Fatalf("New 失敗: %v", err)
		}
		ids[i] = id
	}
	// 前 8 個位元組（毫秒時間戳＋次毫秒計數）必須嚴格遞增，否則以標識排序的
	// 遊標分頁與日誌比對將失去意義；同一毫秒內由次毫秒計數接力保證單調。
	for i := 1; i < len(ids); i++ {
		prev, cur := orderPrefix(ids[i-1].String()), orderPrefix(ids[i].String())
		if prev >= cur {
			t.Fatalf("排序前綴未嚴格遞增：%s 之後是 %s", prev, cur)
		}
	}
	assertAllUnique(t, ids)
}

// orderPrefix 回傳正規字串去掉連字號後的前 16 個十六進位字元（即決定順序的前 8 個位元組，
// 其中第 13 個字元恆為版本 7）。
func orderPrefix(canonical string) string {
	return strings.ReplaceAll(canonical, "-", "")[:16]
}

func TestParseRejectsNonCanonical(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"空字串", "", "長度應為"},
		{"過短", sampleV7[:35], "長度應為"},
		{"過長", sampleV7 + "0", "長度應為"},
		{"大寫", strings.ToUpper(sampleV7), "小寫正規格式"},
		{"花括號", "{" + sampleV7 + "}", "長度應為"},
		{"urn 前綴", "urn:uuid:" + sampleV7, "長度應為"},
		{"無連字號", strings.ReplaceAll(sampleV7, "-", ""), "長度應為"},
		{"非十六進位字元", "018f6b3z-2c1d-7e4f-8a9b-0c1d2e3f4a5b", "格式不合法"},
		{"連字號位置錯誤", "018f6b3a2-c1d-7e4f-8a9b-0c1d2e3f4a5b", "格式不合法"},
		{"尾隨空白", sampleV7 + " ", "長度應為"},
		{"版本 4", "3f2504e0-4f89-41d3-9a0c-0305e82c3301", "VERSION_4"},
		{"版本 6", "018f6b3a-2c1d-6e4f-8a9b-0c1d2e3f4a5b", "VERSION_6"},
		{"版本 1", "1a2b3c4d-5e6f-1a2b-3c4d-5e6f7a8b9c0d", "VERSION_1"},
		{"變體非 RFC 4122", "018f6b3a-2c1d-7e4f-0a9b-0c1d2e3f4a5b", "變體位元"},
		{"零值標識", "00000000-0000-0000-0000-000000000000", "VERSION_0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := Parse(tc.input)
			if err == nil {
				t.Fatalf("應拒絕 %q，實際解析為 %v", tc.input, id)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("錯誤訊息應含 %q，實際 %q", tc.want, err)
			}
			if !id.IsNil() {
				t.Errorf("解析失敗時應回傳未指派的零值標識，實際 %v", id)
			}
		})
	}
}

func TestParseAcceptsCanonicalV7(t *testing.T) {
	id, err := Parse(sampleV7)
	if err != nil {
		t.Fatalf("正規格式應可解析: %v", err)
	}
	if got := id.String(); got != sampleV7 {
		t.Fatalf("字串表示應保持 %q，實際 %q", sampleV7, got)
	}
}

func TestNilIDRoundTripsOnlyOutward(t *testing.T) {
	if got := Nil.String(); got != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("零值標識的字串表示異常: %q", got)
	}
	if !Nil.IsNil() {
		t.Fatal("Nil 應為未指派狀態")
	}
	// 零值不是 UUIDv7，因此無法經 Parse 或 JSON 讀回：未指派的標識不會混進業務資料。
	if _, err := Parse(Nil.String()); err == nil {
		t.Fatal("零值標識不應可被解析")
	}
}

func TestJSONUsesStringRepresentation(t *testing.T) {
	id, err := Parse(sampleV7)
	if err != nil {
		t.Fatalf("Parse 失敗: %v", err)
	}
	payload, err := json.Marshal(struct {
		ID ID `json:"id"`
	}{ID: id})
	if err != nil {
		t.Fatalf("編碼失敗: %v", err)
	}
	if want := `{"id":"` + sampleV7 + `"}`; string(payload) != want {
		t.Fatalf("JSON 表示應為 %s，實際 %s", want, payload)
	}

	var got struct {
		ID ID `json:"id"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("解碼失敗: %v", err)
	}
	if got.ID != id {
		t.Fatalf("往返回值不相等，期望 %v，實際 %v", id, got.ID)
	}
}

func TestJSONRejectsInvalidID(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"數值型別", `{"id":12345}`},
		{"整數形式的標識", `{"id":189302345678901234}`},
		{"大寫", `{"id":"018F6B3A-2C1D-7E4F-8A9B-0C1D2E3F4A5B"}`},
		{"版本 4", `{"id":"3f2504e0-4f89-41d3-9a0c-0305e82c3301"}`},
		{"自訂拼接字串", `{"id":"TX-018f6b3a"}`},
		{"null", `{"id":null}`},
		{"物件", `{"id":{"uuid":"` + sampleV7 + `"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got struct {
				ID ID `json:"id"`
			}
			if err := json.Unmarshal([]byte(tc.input), &got); err == nil {
				t.Fatalf("應拒絕 %s，實際解析為 %v", tc.input, got.ID)
			}
			if !got.ID.IsNil() {
				t.Errorf("解碼失敗後不得留下有效標識，實際 %v", got.ID)
			}
		})
	}
}

func TestConcurrentNewProducesNoDuplicates(t *testing.T) {
	const workers, perWorker = 8, 2500
	batches := make([][]ID, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ids := make([]ID, perWorker)
			for j := range ids {
				id, err := New()
				if err != nil {
					t.Errorf("第 %d 個工作執行緒 New 失敗: %v", idx, err)
					return
				}
				ids[j] = id
			}
			batches[idx] = ids
		}(i)
	}
	wg.Wait()

	all := make([]ID, 0, workers*perWorker)
	for _, ids := range batches {
		all = append(all, ids...)
	}
	assertAllUnique(t, all)
}

// assertAllUnique 確認標識清單無重複（協議要求內部主鍵全域唯一）。
func assertAllUnique(t *testing.T, ids []ID) {
	t.Helper()
	seen := make(map[ID]int, len(ids))
	for i, id := range ids {
		if first, ok := seen[id]; ok {
			t.Fatalf("第 %d 個標識與第 %d 個重複: %s", i, first, id)
		}
		seen[id] = i
	}
}

// TestStringFormat 確認 ID 可直接以 %v / %s 輸出正規字串，
// 讓呼叫端不需要另外存取內部欄位即可取得協議表示。
func TestStringFormat(t *testing.T) {
	id, err := Parse(sampleV7)
	if err != nil {
		t.Fatalf("Parse 失敗: %v", err)
	}
	want := sampleV7 + "|" + sampleV7 + "|\"" + sampleV7 + "\""
	if got := fmt.Sprintf("%v|%s|%q", id, id, id); got != want {
		t.Fatalf("格式化輸出異常，期望 %q，實際 %q", want, got)
	}
}
