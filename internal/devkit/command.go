package devkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

// Command 是一道要在指定目錄裡執行、並把輸出原樣交還呼叫方的外部命令。
//
// 開發工具的一半價值在於「看得到過程」：格式化差異、分析訊息、測試進度都必須直接進終端機，
// 而不是被工具重寫一遍。成敗只以退出碼判定，不去解析輸出文字——輸出文字會隨工具鏈版本變。
type Command struct {
	// Desc 是給人看的說明，用於進度列與錯誤訊息。刻意不重述引數：引數值是使用者可任意
	// 給定的文字（例如 --dart-define 或 --plain-name），複寫進日誌與終端輸出沒有必要。
	Desc string
	// Exe 是可執行檔路徑或名稱（由呼叫端解析過）。
	Exe string
	// Args 是引數清單。
	Args []string
}

// Run 在 dir 內執行命令；dir 留空表示沿用目前目錄。
//
// 回傳的錯誤訊息只講「哪一道、怎麼壞的」，工具名前綴由呼叫端加：同一道命令被建置入口與
// 品質入口共用時，訊息裡的品牌不該跟著變。
func (c Command) Run(ctx context.Context, dir string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, c.Exe, c.Args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("%s 失敗（退出碼 %d）", c.Desc, exitErr.ExitCode())
		}
		return fmt.Errorf("%s 無法啟動（%s）：%w", c.Desc, c.Exe, err)
	}
	return nil
}

// lookupExecutable 是 exec.LookPath 的間接層，供測試替換。
//
// Windows 上 exec.LookPath 會依 PATHEXT 補副檔名，實測可把 PATH 上的 flutter 解析成
// flutter.bat；路徑含分隔字元時它直接查該檔案是否存在，因此 --flutter 給絕對路徑同一條路。
var lookupExecutable = exec.LookPath

// LookupExecutable 依名稱或路徑查可執行檔，供呼叫端自行組合候選清單。
func LookupExecutable(name string) (string, error) { return lookupExecutable(name) }

// ErrToolchainMissing 表示命令所需的可執行檔查不到。
var ErrToolchainMissing = errors.New("找不到可執行檔")

// resolveFirst 回傳候選清單中第一個查得到的可執行檔。
func resolveFirst(candidates []string) (string, error) {
	for _, candidate := range candidates {
		if exe, err := lookupExecutable(candidate); err == nil {
			return exe, nil
		}
	}
	return "", fmt.Errorf("%w（依序試過：%s）", ErrToolchainMissing, joinPlain(candidates))
}

// joinPlain 產生供錯誤訊息使用的清單；空白項目寫成「（未設定）」而不是留白，
// 否則訊息裡會出現看不見的洞。
func joinPlain(items []string) string {
	if len(items) == 0 {
		return "（無候選）"
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item == "" {
			out = append(out, "（未設定）")
			continue
		}
		out = append(out, item)
	}
	return fmt.Sprint(out)
}
