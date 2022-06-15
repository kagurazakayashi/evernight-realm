//go:build unix

package database

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile 取得獨佔且不等待的 flock 鎖（Linux/macOS 等部署平台）。
//
// LOCK_NB：取不到鎖立即回報 EWOULDBLOCK，不阻塞啟動。
// flock 以開啟的檔案描述為單位，故同一程序內另開檔案描述重複上鎖同樣會失敗，
// 可作為單寫入實例的判準；程序結束時由作業系統釋放。
func lockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

// unlockFile 釋放先前的獨佔鎖。
func unlockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
