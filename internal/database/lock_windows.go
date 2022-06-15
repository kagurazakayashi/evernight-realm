//go:build windows

package database

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile 對整個檔案取得獨佔且不等待的位元組範圍鎖。
//
// LOCKFILE_FAIL_IMMEDIATELY：取不到鎖立即回報，不阻塞啟動。
// 鎖由檔案控制代碼持有，程序結束（含強制結束）時由作業系統釋放；
// 同一程序內另開控制代碼重複上鎖同樣會失敗，故可作為單寫入實例的判準。
//
// 鎖定範圍刻意放在持有者資訊之後：Windows 的位元組範圍鎖會讓其他控制代碼
// 無法讀取被鎖定的位元組，若鎖在位移 0 則「拒絕第二個寫入者」時無法讀出
// 持有者資訊（pid/取得時間）作為診斷。
func lockFile(file *os.File) error {
	overlapped := holderLockRange()
	return windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &overlapped)
}

// unlockFile 釋放先前的獨佔鎖（範圍須與上鎖時一致）。
func unlockFile(file *os.File) error {
	overlapped := holderLockRange()
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}

// holderLockRange 回傳鎖定範圍（位於持有者資訊區之後）。
func holderLockRange() windows.Overlapped {
	return windows.Overlapped{Offset: holderInfoLimit}
}
