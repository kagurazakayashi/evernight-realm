// Package app 提供服務端啟動入口與版本資訊（STEP-029 最小骨架）。
//
// 啟動函式與 main 分離：main 僅負責組裝與結束碼轉換，
// 實際啟動流程、錯誤處理與日後各模組的組合皆以本包為核心。
package app

import "fmt"

// Version 為目前開發版本。正式版號策略待發布流程定案後統一管理。
const Version = "0.1.0-dev"

// Run 啟動服務端。目前僅輸出版本 banner 並正常結束；
// 後續步驟將在此加入：配置載入（STEP-030）、資料目錄（STEP-031）、
// HTTP 服務與存活檢查（STEP-032）等。
func Run() error {
	fmt.Printf("evernight-server %s (STEP-029 minimal entry)\n", Version)
	return nil
}
