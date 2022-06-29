package devkit

import (
	"fmt"
	"path/filepath"
)

// FrontendCheckEntryRel 是前端倉庫自有的品質入口（相對子模組目錄）。
//
// 三道前端品質閘（格式化、分析、定向測試）的定義放在前端倉庫裡，根倉庫只負責「找到子模組、
// 把命令叫起來」。理由與內嵌產物的必要檔案清單同源：定義寫兩份時，漏更新的那一份不會讓
// 建置失敗，只會讓兩個入口各自跑出不同的檢查結果。
const FrontendCheckEntryRel = "tools/check/check.dart"

// packageConfigRel 是 `flutter pub get` 的落地標記。
var packageConfigRel = filepath.Join(".dart_tool", "package_config.json")

// FrontendCheckCommand 組出「在子模組目錄執行前端品質入口」的命令。
//
// 為什麼要把 flutter 路徑傳進去：根側已按「指名 → PATH → FLUTTER_ROOT」解析過一次，前端入口
// 若再按自己的規則解析，就可能出現「建置用 A 套 SDK、檢查用 B 套」這種查不出來的差異。
// 注入值放在轉發參數之前，使用者在 `-- ` 之後自己給的 --flutter 因此仍然是最後說得算的那個。
//
// passthrough 原樣附加在最後（例如 --plain-name 的定向測試參數），本函數不解析它：
// 參數語意歸前端入口所有，根側一旦開始挑揀就會出現第二份規則。
func FrontendCheckCommand(frontend Frontend, passthrough []string) (Command, error) {
	entry := filepath.Join(frontend.Dir, filepath.FromSlash(FrontendCheckEntryRel))
	if !FileExists(entry) {
		return Command{}, fmt.Errorf("%w：%s 內找不到品質入口 %s。請先更新前端子模組。",
			ErrSubmoduleMissing, frontend.Dir, FrontendCheckEntryRel)
	}
	// dart run 需要已解析的依賴清單。缺它時 dart 自己的錯誤會指向 .dart_tool 目錄，
	// 看不出真正缺的是一行 pub get，因此在叫起來之前先把話說清楚（只報告，不代跑）。
	if !FileExists(filepath.Join(frontend.Dir, packageConfigRel)) {
		return Command{}, fmt.Errorf("前端依賴尚未解析：%s 內找不到 %s。請先在 %s 執行 flutter pub get。",
			frontend.Dir, filepath.ToSlash(packageConfigRel), frontend.Dir)
	}

	dartExe, err := DartExecutable(frontend.FlutterExe)
	if err != nil {
		return Command{}, err
	}

	args := []string{"run", FrontendCheckEntryRel, flutterFlag, frontend.FlutterExe}
	args = append(args, passthrough...)
	return Command{Desc: "前端品質入口", Exe: dartExe, Args: args}, nil
}

// flutterFlag 是前端品質入口接收 flutter 路徑的旗標名。兩側共用這個寫法，
// 因此它出現在這裡而不是散在字串拼接中。
const flutterFlag = "--flutter"
