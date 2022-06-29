package devkit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Frontend 為通過前置檢查後的 Flutter 前端目標。
type Frontend struct {
	// Module 為 .gitmodules 內的子模組名稱，只用於訊息。
	Module string
	// Dir 是實際的子模組目錄（絕對路徑），前端命令一律在此執行。
	Dir string
	// FlutterExe 是解析出的 flutter 可執行檔路徑。
	FlutterExe string
}

// PrepareFrontend 檢查前端「存在且可用」，回傳命令執行所需的路徑。
//
// 只讀不寫，也不執行任何 git 命令；不比對子模組 HEAD 與根倉庫指標——指標漂移在正常流程裡
// 幾乎必然出現，把它當成失敗會讓入口在健康狀態下拒絕工作。
func PrepareFrontend(root string, entry SubmoduleEntry, flutterFlag string) (Frontend, error) {
	appDir, err := SubmoduleDir(root, entry)
	if err != nil {
		return Frontend{}, err
	}

	info, err := os.Stat(appDir)
	switch {
	case err != nil:
		return Frontend{}, fmt.Errorf("%w：%s 不存在。請自行初始化子模組後重試；本工具不代下載、不改動檢出版本",
			ErrSubmoduleMissing, appDir)
	case !info.IsDir():
		return Frontend{}, fmt.Errorf("%w：%s 不是目錄", ErrSubmoduleMissing, appDir)
	}

	children, err := os.ReadDir(appDir)
	if err != nil {
		return Frontend{}, fmt.Errorf("無法讀取子模組目錄 %s：%w", appDir, err)
	}
	if len(children) == 0 {
		return Frontend{}, fmt.Errorf("%w：%s 是空目錄（尚未檢出）。請自行初始化子模組後重試；本工具不代下載、不改動檢出版本",
			ErrSubmoduleMissing, appDir)
	}
	if !FileExists(filepath.Join(appDir, PubspecFile)) {
		return Frontend{}, fmt.Errorf("%w：%s 內找不到 %s，目錄內容與 Flutter 前端不符",
			ErrSubmoduleMissing, appDir, PubspecFile)
	}

	exe, err := ResolveFlutter(flutterFlag)
	if err != nil {
		return Frontend{}, err
	}
	return Frontend{Module: entry.Name, Dir: appDir, FlutterExe: exe}, nil
}

// planFlutterLookup 回傳查 flutter 可執行檔的候選順序（純函數，便於測試）。
//
// 指名優先；否則先 PATH，再退回 FLUTTER_ROOT/bin。PATH 優先是因為既有的開發腳本都以 PATH
// 決定用哪套工具鏈，FLUTTER_ROOT 只當它沒設時的備援。
func planFlutterLookup(explicit, flutterRoot string) []string {
	if target := strings.TrimSpace(explicit); target != "" {
		return []string{target}
	}
	candidates := []string{"flutter"}
	if root := strings.TrimSpace(flutterRoot); root != "" {
		candidates = append(candidates, filepath.Join(root, "bin", "flutter"))
	}
	return candidates
}

// ResolveFlutter 依 planFlutterLookup 的順序取得第一個存在的可執行檔。
func ResolveFlutter(explicit string) (string, error) {
	candidates := planFlutterLookup(explicit, os.Getenv("FLUTTER_ROOT"))
	exe, err := resolveFirst(candidates)
	if err == nil {
		return exe, nil
	}
	return "", fmt.Errorf("%w；請以 --flutter 指定路徑（查過：%s）", err, strings.Join(candidates, "、"))
}

// DartExecutable 取得與 flutter 同套工具鏈的 dart 可執行檔。
//
// 不直接用 PATH 上的 dart：那可能是另一套 SDK 的 dart，品質閘的結果就會和建置結果來自
// 兩套工具鏈。同目錄推不出來時才退回 PATH，並在錯誤訊息裡說清楚試過哪些。
func DartExecutable(flutterExe string) (string, error) {
	var tried []string
	if name := dartAlongside(flutterExe); name != "" {
		tried = append(tried, name)
		if FileExists(name) {
			return name, nil
		}
	}
	tried = append(tried, "dart")
	if exe, err := lookupExecutable("dart"); err == nil {
		return exe, nil
	}
	return "", fmt.Errorf("找不到 dart 可執行檔（依序試過：%s）", strings.Join(tried, "、"))
}

// dartAlongside 由 flutter 可執行檔路徑推得同目錄的 dart 路徑（純函數，便於測試）。
//
// 保留原檔名的其他前後綴與副檔名，是為兼容 flutter／flutter.bat／flutter.exe 三種寫法。
// 推不出來（檔名不是 flutter 開頭）時回傳空字串，交回呼叫端退回 PATH。
func dartAlongside(flutterExe string) string {
	dir, base := filepath.Split(flutterExe)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)

	switch {
	case strings.HasPrefix(name, "flutter"):
		name = "dart" + strings.TrimPrefix(name, "flutter")
	case strings.HasPrefix(name, "Flutter"):
		name = "Dart" + strings.TrimPrefix(name, "Flutter")
	default:
		return ""
	}
	return dir + name + ext
}

// planGoLookup 回傳查 go 可執行檔的候選順序（純函數，便於測試）。
//
// 順序與 flutter 一致：指名 → PATH → GOROOT/bin。GOROOT 作為備援尤其重要，因為本套件的
// 呼叫端常常就是被 `go run` 起的，那個環境裡 GOROOT 必定指著正在使用的工具鏈。
func planGoLookup(explicit, goRoot string) []string {
	if target := strings.TrimSpace(explicit); target != "" {
		return []string{target}
	}
	candidates := []string{"go"}
	if root := strings.TrimSpace(goRoot); root != "" {
		candidates = append(candidates, filepath.Join(root, "bin", "go"))
	}
	return candidates
}

// ResolveGo 依 planGoLookup 的順序取得 go 可執行檔。
func ResolveGo(explicit string) (string, error) {
	candidates := planGoLookup(explicit, os.Getenv("GOROOT"))
	exe, err := resolveFirst(candidates)
	if err == nil {
		return exe, nil
	}
	return "", fmt.Errorf("%w；請以 --go 指定路徑（查過：%s）", err, strings.Join(candidates, "、"))
}

// GoFmtExecutable 取得與 go 同套工具鏈的 gofmt 可執行檔。
//
// gofmt 是獨立的可執行檔（在 GOROOT/bin），不是 `go fmt` 子命令：後者已廢棄且多繞一層，
// 而這裡要的只是「同一套工具鏈裡那個 gofmt」。同目錄找不到時退回 PATH。
func GoFmtExecutable(goExe string) (string, error) {
	var tried []string
	if name := gofmtAlongside(goExe); name != "" {
		tried = append(tried, name)
		if FileExists(name) {
			return name, nil
		}
	}
	tried = append(tried, "gofmt")
	if exe, err := lookupExecutable("gofmt"); err == nil {
		return exe, nil
	}
	return "", fmt.Errorf("找不到 gofmt 可執行檔（依序試過：%s）", strings.Join(tried, "、"))
}

// gofmtAlongside 由 go 可執行檔路徑推得同目錄的 gofmt 路徑（純函數，便於測試）。
//
// 檔名不是 go 時（例如自 PATH 查出的 go1.27.1.exe，或絕對路徑寫到 bin 目錄）仍取同目錄，
// 因為 go 與 gofmt 必在同一個 bin 裡。
func gofmtAlongside(goExe string) string {
	dir, base := filepath.Split(goExe)
	if dir == "" && base == "" {
		return ""
	}
	return dir + "gofmt" + filepath.Ext(base)
}
