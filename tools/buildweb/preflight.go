// Package main 提供 evernight-realm 根倉庫的 Flutter Web 建置入口。
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// gitmodulesFile 是 Git 記錄子模組清單的檔名，也是本工具定位前端目錄的唯一依據。
const gitmodulesFile = ".gitmodules"

// pubspecFile 是 Flutter 專案的身分憑證：目錄在但此檔不在，代表那不是可建置的前端。
const pubspecFile = "pubspec.yaml"

// frontend 為通過前置檢查後的建置目標。
type frontend struct {
	// module 為 .gitmodules 內的子模組名稱，只用於訊息。
	module string
	// appDir 是實際的子模組目錄（絕對路徑），flutter 命令一律在此執行。
	appDir string
	// flutterExe 是解析出的 flutter 可執行檔路徑。
	flutterExe string
}

// ErrSubmoduleMissing 表示前端子模組不存在或內容為空。
//
// 單獨設一個哨兵錯誤，是為了讓呼叫端（與讀錯誤的人）一眼分辨「該去初始化子模組」與
// 「建置過程壞了」。本工具在任何情況下都不代初始化、不下載、不改動檢出版本。
var ErrSubmoduleMissing = errors.New("前端子模組未就緒")

// resolveRepoRoot 取得根倉庫目錄。未指名時自目前目錄向上找 .gitmodules。
func resolveRepoRoot(explicit string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("buildweb: 無法取得目前目錄：%w", err)
	}
	return resolveRepoRootFrom(cwd, explicit)
}

// resolveRepoRootFrom 為 resolveRepoRoot 的可測試版本：起點目錄由參數給定。
//
// 向上找而非假設「倉庫根就是執行目錄」，是為了讓 `go run ./tools/buildweb` 在子目錄裡
// 也能跑；但一律以 .gitmodules 的存在為準，找不到就停止，不去猜哪個目錄是倉庫根。
func resolveRepoRootFrom(start, explicit string) (string, error) {
	if target := strings.TrimSpace(explicit); target != "" {
		abs, err := filepath.Abs(target)
		if err != nil {
			return "", fmt.Errorf("buildweb: 無法解析 --repo-root：%w", err)
		}
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			return "", fmt.Errorf("buildweb: --repo-root 不是目錄：%s", abs)
		}
		if !fileExists(filepath.Join(abs, gitmodulesFile)) {
			return "", fmt.Errorf("buildweb: %s 內找不到 %s，無法定位前端子模組", abs, gitmodulesFile)
		}
		return abs, nil
	}

	dir, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("buildweb: 無法解析目錄：%w", err)
	}
	for {
		if fileExists(filepath.Join(dir, gitmodulesFile)) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("buildweb: 自 %s 向上都找不到 %s；請在根倉庫內執行，或以 --repo-root 指定", start, gitmodulesFile)
		}
		dir = parent
	}
}

// submoduleEntry 為 .gitmodules 中的一筆記錄。
type submoduleEntry struct {
	name string
	path string
}

// readSubmodules 讀取並解析倉庫根的 .gitmodules。
func readSubmodules(root string) ([]submoduleEntry, error) {
	content, err := os.ReadFile(filepath.Join(root, gitmodulesFile))
	if err != nil {
		return nil, fmt.Errorf("buildweb: 讀取 %s 失敗：%w", gitmodulesFile, err)
	}
	entries := parseGitmodules(string(content))
	if len(entries) == 0 {
		return nil, fmt.Errorf("buildweb: %s 內沒有任何子模組記錄", gitmodulesFile)
	}
	return entries, nil
}

// parseGitmodules 解析 .gitmodules 的 name 與 path。
//
// 只認 Git 自己寫出的那個格式（[submodule "名稱"] 之後的 `key = value`），不支援巨集、
// 不支援多行值，也不引入 INI 套件：這裡要的是讀自己倉庫裡那兩行，多出來的解析能力只會
// 多一個會出錯的地方。
func parseGitmodules(content string) []submoduleEntry {
	var entries []submoduleEntry
	var current *submoduleEntry

	flush := func() {
		if current != nil && current.path != "" {
			entries = append(entries, *current)
		}
		current = nil
	}

	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			flush()
			current = &submoduleEntry{name: sectionName(line)}
			continue
		}
		if current == nil {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(key) == "path" {
			current.path = unquote(strings.TrimSpace(value))
		}
	}
	flush()
	return entries
}

// sectionName 取 [submodule "evernight-realm-app"] 裡的引號內容；取不到回傳空字串。
func sectionName(line string) string {
	parts := strings.Split(line, `"`)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// unquote 去掉組態值兩側的成對雙引號。
func unquote(value string) string {
	if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		return value[1 : len(value)-1]
	}
	return value
}

// selectSubmodule 依 --module 指名挑定子模組記錄。
//
// 未指名時要求「恰好一筆」：本工具是為這個倉庫的前端服務的，多倉庫情況下去猜哪一個是
// Web 應用（比對名稱裡有沒有 app）只會把選錯的風險藏進工具裡。
func selectSubmodule(entries []submoduleEntry, wanted string) (submoduleEntry, error) {
	if target := strings.TrimSpace(wanted); target != "" {
		var matches []submoduleEntry
		for _, entry := range entries {
			if strings.EqualFold(entry.name, target) || filepath.ToSlash(entry.path) == filepath.ToSlash(target) {
				matches = append(matches, entry)
			}
		}
		switch len(matches) {
		case 1:
			return matches[0], nil
		case 0:
			return submoduleEntry{}, fmt.Errorf("buildweb: .gitmodules 內沒有名為或路徑為 %q 的子模組（現有：%s）",
				target, joinNames(entries))
		default:
			return submoduleEntry{}, fmt.Errorf("buildweb: --module %q 同時對上多筆子模組記錄", target)
		}
	}
	if len(entries) != 1 {
		return submoduleEntry{}, fmt.Errorf("buildweb: .gitmodules 內有 %d 筆子模組，請以 --module 指名（現有：%s）",
			len(entries), joinNames(entries))
	}
	return entries[0], nil
}

// joinNames 產生供錯誤訊息使用的子模組名稱清單。
func joinNames(entries []submoduleEntry) string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, fmt.Sprintf("%s→%s", entry.name, entry.path))
	}
	return strings.Join(names, "、")
}

// preflight 檢查建置目標存在且可用。本函數只讀不寫，也不執行任何 git 命令。
func preflight(root string, entry submoduleEntry, flutterFlag string) (frontend, error) {
	appDir, err := submoduleDir(root, entry)
	if err != nil {
		return frontend{}, err
	}

	info, err := os.Stat(appDir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return frontend{}, fmt.Errorf("%w：%s 不存在。請自行初始化子模組後重試；本工具不代下載、不改動檢出版本",
			ErrSubmoduleMissing, appDir)
	case err != nil:
		return frontend{}, fmt.Errorf("buildweb: 無法讀取子模組目錄 %s：%w", appDir, err)
	case !info.IsDir():
		return frontend{}, fmt.Errorf("%w：%s 不是目錄", ErrSubmoduleMissing, appDir)
	}

	children, err := os.ReadDir(appDir)
	if err != nil {
		return frontend{}, fmt.Errorf("buildweb: 無法讀取子模組目錄 %s：%w", appDir, err)
	}
	if len(children) == 0 {
		return frontend{}, fmt.Errorf("%w：%s 是空目錄（尚未檢出）。請自行初始化子模組後重試；本工具不代下載、不改動檢出版本",
			ErrSubmoduleMissing, appDir)
	}
	if !fileExists(filepath.Join(appDir, pubspecFile)) {
		return frontend{}, fmt.Errorf("%w：%s 內找不到 %s，目錄內容與 Flutter 前端不符",
			ErrSubmoduleMissing, appDir, pubspecFile)
	}

	exe, err := resolveFlutter(flutterFlag)
	if err != nil {
		return frontend{}, err
	}
	return frontend{module: entry.name, appDir: appDir, flutterExe: exe}, nil
}

// planFlutterLookup 回傳查 flutter 可執行檔的候選順序（純函數，便於測試）。
//
// 指名優先；否則先 PATH，再退回 FLUTTER_ROOT/bin。PATH 優先是因為既有的開發腳本與 CI
// 都以 PATH 決定用哪套工具鏈，FLUTTER_ROOT 只當它沒設時的備援。
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

// resolveFlutter 依 planFlutterLookup 的順序取得第一個存在的可執行檔。
//
// exec.LookPath 在 Windows 會依 PATHEXT 補上副檔名，實測可把 PATH 上的 flutter 解析成
// flutter.bat，因此候選清單不需要自己塞 .bat 變體。
func resolveFlutter(explicit string) (string, error) {
	candidates := planFlutterLookup(explicit, os.Getenv("FLUTTER_ROOT"))
	for _, candidate := range candidates {
		exe, err := lookupExecutable(candidate)
		if err == nil {
			return exe, nil
		}
	}
	return "", fmt.Errorf("buildweb: 找不到 flutter 可執行檔（依序試過：%s）；請以 --flutter 指定路徑",
		strings.Join(candidates, "、"))
}

// lookupExecutable 是 exec.LookPath 的間接層，供測試替換。
//
// Windows 上 exec.LookPath 會依 PATHEXT 補副檔名，實測可把 PATH 上的 flutter 解析成
// flutter.bat；路徑含分隔字元時它直接查該檔案是否存在，因此 --flutter 給絕對路徑同一條路。
var lookupExecutable = exec.LookPath

// submoduleDir 由 .gitmodules 記錄的相對路徑算出子模組目錄（絕對路徑）。
//
// 路徑取自倉庫內的組態檔，因此仍要做越界檢查：一旦有人把 path 寫成 "../別處"，
// 建置工具就會跑到倉庫外面去讀目錄、甚至（預設清空產物目錄那一步）刪除東西。
func submoduleDir(root string, entry submoduleEntry) (string, error) {
	rel := filepath.ToSlash(strings.TrimSpace(entry.path))
	if rel == "" {
		return "", fmt.Errorf("buildweb: .gitmodules 中子模組 %q 缺少 path 記錄", entry.name)
	}
	appDir := filepath.Clean(filepath.Join(filepath.Clean(root), filepath.FromSlash(rel)))
	if !containsPath(root, appDir) || appDir == filepath.Clean(root) {
		return "", fmt.Errorf("%w：.gitmodules 記錄的路徑 %q 不在倉庫根之內", ErrSubmoduleMissing, entry.path)
	}
	return appDir, nil
}

// containsPath 回傳 outer 是否等於 inner、或為 inner 的祖先目錄。兩端都先過 Clean。
func containsPath(outer, inner string) bool {
	rel, err := filepath.Rel(filepath.Clean(outer), filepath.Clean(inner))
	if err != nil {
		return false
	}
	up := ".." + string(filepath.Separator)
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, up))
}

// resolveOutput 決定產物目錄，並確認它是一個本工具可以有條件清空的目錄。
//
// 三條限制各有理由：落在倉庫根之外，go:embed 讀不到（它不能引用上級目錄）；等於倉庫根，
// 清空動作等於把整個倉庫端掉；與子模組互相包含，清空時會連前端一起去掉。相對路徑一律
// 以倉庫根為基準，這樣在子目錄執行也不會出現「同樣的命令寫到兩個地方」。
func resolveOutput(root, value, appDir string) (string, error) {
	target := strings.TrimSpace(value)
	if target == "" {
		return "", errors.New("buildweb: 產物目錄不可為空白")
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Clean(root), filepath.FromSlash(target))
	}
	out := filepath.Clean(target)

	rootClean := filepath.Clean(root)
	if out == rootClean || !containsPath(rootClean, out) {
		return "", fmt.Errorf("buildweb: 產物目錄必須落在倉庫根之內且不得為倉庫根本身：%s", out)
	}
	if containsPath(out, appDir) {
		return "", fmt.Errorf("buildweb: 產物目錄 %s 會包住前端子模組 %s，建置前的清空動作會連同子模組一併刪除", out, appDir)
	}
	if containsPath(appDir, out) {
		return "", fmt.Errorf("buildweb: 產物目錄 %s 位於前端子模組之內，請改用倉庫根下的目錄（預設 %s）", out, defaultOutputRel)
	}
	return out, nil
}

// fileExists 回傳路徑是否存在（不區分檔案與目錄）。
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
