package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kagurazakayashi/evernight-realm/internal/webassets/bundle"
)

// noWebResourcesCDN 是本工具存在的理由：少了它，release 產物的 CanvasKit 會改向
// gstatic CDN 取檔，離線部署的首屏直接白屏。
const noWebResourcesCDN = "--no-web-resources-cdn"

// dartDefineFlag 為 flutter 的編譯期參數旗標，一律用 `--dart-define=K=V` 單詞寫法。
const dartDefineFlag = "--dart-define="

// flutterCommand 是一道要在子模組目錄執行的命令。
type flutterCommand struct {
	// desc 是給人看的說明，同時用於錯誤訊息。刻意不重述引數：--dart-define 的值是
	// 使用者可任意給定的文字，把它複寫進日誌與終端輸出沒有必要。
	desc string
	exe  string
	args []string
}

// run 在 dir 內執行命令並把輸出原樣轉接給呼叫方（建置工具的價值一半在於看得到過程）。
func (c flutterCommand) run(ctx context.Context, dir string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, c.exe, c.args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("buildweb: %s 失敗（退出碼 %d）", c.desc, exitErr.ExitCode())
		}
		return fmt.Errorf("buildweb: %s 無法啟動（%s）：%w", c.desc, c.exe, err)
	}
	return nil
}

// buildArgs 組出 `flutter build web` 的引數（純函數，便於測試）。
//
// 模式只開 release／debug 兩檔，不給 profile：profile 產物不是可部署的網頁。
// 輸出目錄以 -o 直給絕對路徑，讓產物一次就落在內嵌位置，不留第二份。
func buildArgs(out string, o options) []string {
	mode := "--release"
	if o.debug {
		mode = "--debug"
	}
	args := []string{"build", "web", mode, noWebResourcesCDN, "-o", out}
	for _, define := range o.defines {
		args = append(args, dartDefineFlag+define)
	}
	return args
}

// qualityArgs 依序回傳三道品質閘的引數（純函數，便於測試）。
//
// 順序是「便宜且訊息最直的在前」：格式差異通常一行就講完，分析要看程式碼，
// 測試最慢，放在最後，讓人在放棄等待之前先看到可修的東西。
func qualityArgs() [][]string {
	return [][]string{
		{"format", "--output=none", "--set-exit-if-changed", "."},
		{"analyze"},
		{"test"},
	}
}

// qualityCommands 把三道品質閘包成可執行命令。dart 由 flutter 的同目錄推得。
func qualityCommands(flutterExe string) ([]flutterCommand, error) {
	dartExe, err := dartExecutable(flutterExe)
	if err != nil {
		return nil, err
	}

	args := qualityArgs()
	gates := []flutterCommand{
		{desc: "dart format", exe: dartExe, args: args[0]},
		{desc: "flutter analyze", exe: flutterExe, args: args[1]},
		{desc: "flutter test", exe: flutterExe, args: args[2]},
	}
	return gates, nil
}

// dartExecutable 取得與 flutter 同套工具鏈的 dart 可執行檔。
//
// 不直接用 PATH 上的 dart：那可能是另一套 SDK 的 dart，品質閘的結果就會和建置結果
// 來自不同工具鏈。同目錄推不出來時才退回 PATH，並在錯誤訊息裡說清楚試過哪些。
func dartExecutable(flutterExe string) (string, error) {
	var tried []string
	if name := dartAlongside(flutterExe); name != "" {
		tried = append(tried, name)
		if fileExists(name) {
			return name, nil
		}
	}
	tried = append(tried, "dart")
	if exe, err := lookupExecutable("dart"); err == nil {
		return exe, nil
	}
	return "", fmt.Errorf("buildweb: 找不到 dart 可執行檔（依序試過：%s）", strings.Join(tried, "、"))
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

// cleanOutput 清空產物目錄，並在清空的結果上補回佔位檔。
//
// 為什麼要清：產物最終會被 go:embed 塞進單一執行檔，上一次的殘留檔案（例如改名的字型
// 或舊版 main.dart.js）不會自己消失，只會被一起內嵌進發布檔。flutter 自己不清輸出目錄，
// 所以這一步由本工具負責；路徑已在 resolveOutput 限制於倉庫根內且與子模組互不包含。
//
// 為什麼要補佔位檔：go:embed 匹配不到任何檔案時是「編譯失敗」，而產物目錄不進版本庫。
// 清空後若建置失敗（或根本還沒建置過），留一個空目錄會讓整個後端連編譯都過不了，
// 而錯誤訊息指向的是 go:embed，看不出真正缺的是前端建置。補一個佔位檔讓「前端沒建置」
// 表達成啟動摘要裡的一句說明，而不是一條跟前端無關的編譯錯誤。
func cleanOutput(out string, stdout io.Writer) error {
	info, err := os.Stat(out)
	if errors.Is(err, os.ErrNotExist) {
		return writePlaceholder(out, stdout)
	}
	if err != nil {
		return fmt.Errorf("buildweb: 無法檢查產物目錄 %s：%w", out, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("buildweb: 產物目錄 %s 已存在但不是目錄", out)
	}
	if err := os.RemoveAll(out); err != nil {
		return fmt.Errorf("buildweb: 清空產物目錄 %s 失敗：%w", out, err)
	}
	fmt.Fprintf(stdout, "已清空舊產物：%s\n", out)
	return writePlaceholder(out, stdout)
}

// writePlaceholder 建立產物目錄並寫入佔位檔；內容取自判定標準套件，不在這裡複寫。
func writePlaceholder(out string, stdout io.Writer) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return fmt.Errorf("buildweb: 建立產物目錄 %s 失敗：%w", out, err)
	}
	path := filepath.Join(out, bundle.PlaceholderName)
	if err := os.WriteFile(path, []byte(bundle.PlaceholderNote), 0o644); err != nil {
		return fmt.Errorf("buildweb: 寫入佔位檔 %s 失敗：%w", path, err)
	}
	fmt.Fprintf(stdout, "已在產物目錄補回佔位檔：%s\n", path)
	return nil
}
