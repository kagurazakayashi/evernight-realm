// Package app 提供服務端啟動入口與版本資訊（最小骨架）。
//
// 啟動函式與 main 分離：main 僅負責組裝與結束碼轉換，
// 實際啟動流程、錯誤處理與日後各模組的組合皆以本包為核心。
package app

import (
	"fmt"

	"github.com/kagurazakayashi/evernight-realm/internal/config"
)

// Version 為目前開發版本。正式版號策略待發布流程定案後統一管理。
const Version = "0.1.0-dev"

// Run 啟動服務端。流程：解析命令列 → 載入並校驗組態 → 路徑規範化
// → 資料目錄初始化（建目錄/可寫性探測/範例組態）→ 輸出脫敏摘要與監聽提示。
// 組態非法或目錄不可用時回傳錯誤（含欄位路徑，不含機密），由 main 決定結束碼。
func Run(args []string) error {
	opts, err := config.ParseArgs(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(opts)
	if err != nil {
		return err
	}
	if err := cfg.Resolve(); err != nil {
		return err
	}
	if err := cfg.Prepare(); err != nil {
		return err
	}

	fmt.Printf("evernight-server %s\n", Version)
	fmt.Println(cfg.Redacted())
	fmt.Printf("實際監聽：%s\n", cfg.Server.Listen)
	if cfg.ListenAllInterfaces() {
		fmt.Println("風險提示：監聽地址暴露於所有介面（含公網網卡），請確認防火牆與部署範圍。")
	}
	return nil
}
