//go:build !windows && !unix

package database

import (
	"errors"
	"os"
)

// lockFile 於未支援的平台一律失敗（fail closed）。
//
// 單寫入實例約束是資料完整性前提（規格 §26.3），
// 無法確認可強制執行時寧可拒絕啟動，也不允許兩個程序同時寫入。
func lockFile(*os.File) error {
	return errors.New("此平台不支援檔案鎖，無法保證單寫入實例約束")
}

// unlockFile 於未支援的平台不會有鎖可釋放。
func unlockFile(*os.File) error {
	return nil
}
