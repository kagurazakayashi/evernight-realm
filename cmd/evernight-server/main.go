// Package main 為長夜幻境（Evernight Realm）服務端程式 evernight-server 的進入點。
//
// 本檔案保持「薄殼」（STEP-029）：僅組裝並呼叫 internal/app.Run，
// 啟動與業務邏輯分離；配置載入（STEP-030）、資料目錄（STEP-031）、
// HTTP 服務（STEP-032）於後續步驟逐步加入 internal/app 及其依賴。
package main

import (
	"fmt"
	"os"

	"github.com/kagurazakayashi/evernight-realm/internal/app"
)

// main 為程式進入點：以 app.Run 的回傳錯誤決定結束碼。
func main() {
	if err := app.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
