package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// requiredWebFiles 是判定「這是一份可用的 Flutter Web 產物」的必要檔案清單。
//
// 清單以觀測到的實際產物為準，只收 Web 引擎與資源索引必要的檔案，不收應用自己的資產
// （字型、圖片）：後兩者的名字會隨前端功能變動，把它們寫死在建置工具裡，只會讓下一次
// 正常的前端改動變成一條看不懂的建置失敗。是否完全離線另行以斷網驗證（屬驗證步驟，
// 不在本工具職責內）。
var requiredWebFiles = []string{
	"index.html",
	"flutter.js",
	"flutter_bootstrap.js",
	"main.dart.js",
	"manifest.json",
	"version.json",
	"assets/AssetManifest.bin.json",
	"canvaskit/canvaskit.js",
	"canvaskit/canvaskit.wasm",
}

// localCanvasKitMarker 是 flutter_bootstrap.js 裡「CanvasKit 走本機」的落地證據。
const localCanvasKitMarker = `"useLocalCanvasKit":true`

// artifactReport 為產物統計，只用於啟動摘要與人工核對體積。
type artifactReport struct {
	files int
	bytes int64
}

// verifyArtifacts 確認產物完整，且本地 CanvasKit 設定確實生效。
//
// 為什麼不能只看 flutter 的退出碼：`--no-web-resources-cdn` 哪天被改名、被設成預設關閉、
// 或被 Flutter 換掉實作，建置照樣回傳成功，但產物會變成向 CDN 取 CanvasKit——那正是本工具
// 要避免的事。所以要在產物上驗證「結果」，而不是只驗證「參數有傳出去」。
func verifyArtifacts(out string) (artifactReport, error) {
	base := filepath.Clean(out)
	for _, rel := range requiredWebFiles {
		path := filepath.Join(base, filepath.FromSlash(rel))
		if !containsPath(base, path) {
			return artifactReport{}, fmt.Errorf("buildweb: 內部檢查清單的路徑寫法超出產物目錄：%s", rel)
		}
		if !fileExists(path) {
			return artifactReport{}, fmt.Errorf("buildweb: 產物不完整，缺少 %s（於 %s）", rel, out)
		}
	}

	bootstrap, err := os.ReadFile(filepath.Join(base, "flutter_bootstrap.js"))
	if err != nil {
		return artifactReport{}, fmt.Errorf("buildweb: 無法讀取 flutter_bootstrap.js：%w", err)
	}
	if !strings.Contains(string(bootstrap), localCanvasKitMarker) {
		return artifactReport{}, fmt.Errorf(
			"buildweb: 產物未採用本機 CanvasKit（flutter_bootstrap.js 內找不到 %s）：此產物在離線環境會向 CDN 取引擎檔案",
			localCanvasKitMarker)
	}

	var report artifactReport
	err = filepath.WalkDir(out, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("讀取 %s：%w", entry.Name(), err)
		}
		report.files++
		report.bytes += info.Size()
		return nil
	})
	if err != nil {
		return artifactReport{}, fmt.Errorf("buildweb: 統計產物失敗：%w", err)
	}
	if report.files == 0 {
		return artifactReport{}, errors.New("buildweb: 產物目錄內沒有任何檔案")
	}
	return report, nil
}

// humanBytes 把位元組數寫成適合終端輸出的大小寫法。
func humanBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	size := float64(bytes)
	for _, name := range units {
		size /= unit
		if size < unit || name == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", size, name)
		}
	}
	return fmt.Sprintf("%d B", bytes)
}
