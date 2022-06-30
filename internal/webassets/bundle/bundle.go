// Package bundle 定義「一份可以對外服務的 Flutter Web 產物」所需滿足的條件。
//
// 判定標準放在這裡而不是放在某一個消費者裡，是因為同一份產物有兩個必須看法一致的場合：
//   - 建置完產物後當場複核（tools/buildweb）；
//   - 執行檔啟動時判斷「我內嵌的這份到底能不能拿出來服務」（internal/webassets）。
//
// 兩處各寫一份清單，遲早會有一個漏更新；漏掉的結果不是建置失敗，而是「建置成功但頁面打不開」，
// 這類失敗最難從訊息回溯。檢查一律以 fs.FS 為輸入，磁碟目錄與內嵌檔案系統共用同一段程式碼。
package bundle

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
)

// RequiredFiles 是判定產物可用的必要檔案清單（相對於產物根目錄，一律用斜線分隔）。
//
// 清單以觀測到的實際產物為準，只收 Web 引擎與資源索引必要的檔案，不收應用自己的資產
// （字型、圖片）：後兩者的名字會隨前端功能變動，把它們寫死在這裡，只會讓下一次
// 正常的前端改動變成一份「看不懂的啟動失敗」。是否完全離線另行以斷網驗證。
var requiredFiles = []string{
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

// Required 回傳必要檔案清單的副本；回傳副本是為了呼叫端（例如測試）改動它時
// 不會把本套件的判定標準一起改掉。
func Required() []string {
	out := make([]string, len(requiredFiles))
	copy(out, requiredFiles)
	return out
}

// BootstrapFile 與 LocalCanvasKitMarker 用於確認 CanvasKit 確實走本機。
//
// 為什麼要查這個：`--no-web-resources-cdn` 哪天被改名、被設成預設關閉、或被 Flutter
// 換掉實作，建置照樣回傳成功，但產物會變成向 CDN 取引擎檔案——那正是離線部署白屏的根因。
// 所以要在產物上驗證「結果」，而不是只驗證「參數有傳出去」。
const (
	BootstrapFile        = "flutter_bootstrap.js"
	LocalCanvasKitMarker = `"useLocalCanvasKit":true`
)

// PlaceholderName 是產物目錄裡的佔位檔名。
//
// go:embed 的樣式匹配不到任何檔案時是「編譯失敗」，而產物目錄不進版本庫，
// 於是一份剛複製下來、還沒建置過前端的倉庫連後端都編不了。留一個佔位檔讓內嵌永遠有東西可嵌，
// 「這份產物到底可不可用」則交由 Verify 在啟動時如實回答——兩者分工不同，不會互相掩蓋。
// 佔位檔刻意以「.」開頭，需要內嵌樣式用 all: 前綴才會連它一起收進來。
const PlaceholderName = ".keep"

// PlaceholderNote 是佔位檔的內容：只說明它存在的理由，不帶任何語意。
//
// 文字由本套件持有，因為「這個檔名代表什麼」是判定標準的一部分；寫入它的兩個場合
// （版本庫裡的副本、建置工具清空產物目錄後的補寫）因此不會各寫一套說法。
const PlaceholderNote = "佔位檔：讓前端尚未建置時 go:embed 仍能編譯。\n" +
	"實際可用性由 internal/webassets 在啟動時判定；本檔不計入產物統計。\n"

// MissingFileError 表示清單中的某個必要檔案不存在。
//
// 以型別承載路徑，讓呼叫端能在自己的訊息裡指出是哪個檔案、在哪個目錄，
// 而不必把本套件的措辭原封不動倒給使用者。
type MissingFileError struct {
	// Path 是相對產物根目錄的檔案路徑（斜線分隔）。
	Path string
}

// Error 回傳缺失說明。
func (e *MissingFileError) Error() string { return fmt.Sprintf("缺少必要檔案 %s", e.Path) }

// ErrNoLocalCanvasKit 表示產物會向 CDN 取引擎檔案，離線環境下首屏白屏。
var ErrNoLocalCanvasKit = errors.New("產物未採用本機 CanvasKit（" + BootstrapFile +
	" 內找不到 " + LocalCanvasKitMarker + "）：此產物在離線環境會向 CDN 取引擎檔案")

// Report 為一次檢查的統計，只用於啟動摘要與人工核對體積。
type Report struct {
	// Files 為檔案數（不含佔位檔）。
	Files int
	// Bytes 為總位元組數（不含佔位檔）。
	Bytes int64
}

// Verify 檢查 fsys 是否為一份可對外服務的 Flutter Web 產物。
//
// 透過時回傳統計；任何一條條件不符即回傳錯誤且不回傳統計，呼叫端據此決定
// 「不服務網頁」而不是「服務一份壞掉的網頁」。fsys 的根即產物根目錄。
func Verify(fsys fs.FS) (Report, error) {
	for _, rel := range requiredFiles {
		// 清單是本套件的常數，寫錯屬於程式缺陷；在此攔截而不是讓它變成「找不到檔案」。
		if !fs.ValidPath(rel) {
			return Report{}, fmt.Errorf("bundle: 內部檢查清單的路徑寫法超出產物目錄：%s", rel)
		}
		info, err := fs.Stat(fsys, rel)
		if errors.Is(err, fs.ErrNotExist) {
			return Report{}, &MissingFileError{Path: rel}
		}
		if err != nil {
			return Report{}, fmt.Errorf("bundle: 檢查 %s 失敗：%w", rel, err)
		}
		if info.IsDir() {
			return Report{}, &MissingFileError{Path: rel}
		}
	}

	bootstrap, err := fs.ReadFile(fsys, BootstrapFile)
	if err != nil {
		return Report{}, fmt.Errorf("bundle: 無法讀取 %s：%w", BootstrapFile, err)
	}
	if !bytes.Contains(bootstrap, []byte(LocalCanvasKitMarker)) {
		return Report{}, ErrNoLocalCanvasKit
	}

	return stat(fsys)
}

// HumanBytes 把位元組數寫成適合終端輸出的大小寫法。
func HumanBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return strconv.FormatInt(size, 10) + " B"
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(size)
	for i, name := range units {
		value /= unit
		if value < unit || i == len(units)-1 {
			return fmt.Sprintf("%.1f %s", value, name)
		}
	}
	return strconv.FormatInt(size, 10) + " B"
}

// stat 統計產物檔案數與總位元組數；佔位檔不計入，否則啟動摘要裡的檔案數會比實際產物多一個，
// 讓人以為建置工具多塞了東西。
func stat(fsys fs.FS) (Report, error) {
	var report Report
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || name == PlaceholderName {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("bundle: 讀取 %s 的檔案資訊失敗：%w", name, err)
		}
		report.Files++
		report.Bytes += info.Size()
		return nil
	})
	if err != nil {
		return Report{}, fmt.Errorf("bundle: 統計產物失敗：%w", err)
	}
	return report, nil
}
