// 驗證 UUIDv7 與大整數的協議表示（探針原型，可丟棄）。
//
// 產生 JSON 樣例：內部 ID 用 UUIDv7 標準 36 字元字串（ADR-013），
// 金額/數量以整數最小單位（§8.1）並在 JSON 中以「字串」承載（防 JS Number 失真）。
//
// 用法：go run ./tools/verify/step025-id-roundtrip -out <路徑>
// 輸出：樣例 JSON 寫入 -out 指定檔案。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/google/uuid"
)

type idEntry struct {
	Name  string `json:"name"`
	UUID7 string `json:"uuid7"`
}

type amountEntry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type sample struct {
	Ids     []idEntry     `json:"ids"`
	Amounts []amountEntry `json:"amounts"`
}

func main() {
	out := flag.String("out", "sample.json", "樣例 JSON 輸出路徑")
	flag.Parse()

	// UUIDv7：產生 3 個，確認 version=7 / variant=10 位元。
	ids := make([]idEntry, 0, 3)
	for i := 0; i < 3; i++ {
		u, err := uuid.NewV7()
		if err != nil {
			fmt.Fprintln(os.Stderr, "UUIDv7 產生失敗:", err)
			os.Exit(1)
		}
		ids = append(ids, idEntry{Name: fmt.Sprintf("sample-%d", i+1), UUID7: u.String()})
	}

	// int64 邊界值集合（金額最小單位，§8.1）。
	boundaries := []struct {
		name string
		v    int64
	}{
		{"max_int64", 9223372036854775807},       //  2^63-1
		{"min_int64", -9223372036854775808},      // -2^63
		{"js_safe_max", 9007199254740991},        //  2^53-1（JS Number 精確上限）
		{"js_safe_max_plus_1", 9007199254740992}, //  2^53（JS Number 開始失真）
		{"hundred_k", 100000},                    //  日常量級
		{"zero", 0},
	}
	amounts := make([]amountEntry, 0, len(boundaries))
	for _, b := range boundaries {
		amounts = append(amounts, amountEntry{Name: b.name, Value: strconv.FormatInt(b.v, 10)})
	}

	raw, err := json.MarshalIndent(sample{Ids: ids, Amounts: amounts}, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "JSON 序列化失敗:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, raw, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "寫檔失敗:", err)
		os.Exit(1)
	}
	fmt.Println("樣例已寫入:", *out)
}
