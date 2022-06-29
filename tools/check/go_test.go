package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGofmtTargets(t *testing.T) {
	cases := []struct {
		name    string
		targets []string
		want    []string
	}{
		{name: "未給時為整顆倉庫", targets: nil, want: []string{"."}},
		{name: "./... 折回根目錄", targets: []string{"./..."}, want: []string{"."}},
		{name: "局部遞迴折回該目錄", targets: []string{"./internal/..."}, want: []string{"./internal"}},
		{name: "目錄寫法原樣保留", targets: []string{"./internal/httpapi/"}, want: []string{"./internal/httpapi/"}},
		{name: "多筆去重保序", targets: []string{"./internal/...", "./internal/...", "."}, want: []string{"./internal", "."}},
		{name: "全為空白時回根目錄", targets: []string{"  ", ""}, want: []string{"."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gofmtTargets(tc.targets)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("得到 %v，預期 %v", got, tc.want)
			}
		})
	}
}

func TestGofmtArgsNeverRewrites(t *testing.T) {
	got := strings.Join(gofmtArgs([]string{"./internal/httpapi"}), " ")
	if !strings.HasPrefix(got, "-l ") {
		t.Fatalf("應以 -l 只報告差異: %s", got)
	}
	if strings.Contains(got, "-w") {
		t.Errorf("品質入口不得自動改寫工作樹: %s", got)
	}
}

func TestGofmtHintPointsAtTheSameScope(t *testing.T) {
	hint := gofmtHint([]string{"./internal/..."})
	if !strings.Contains(hint, "gofmt -w") || !strings.Contains(hint, "./internal") {
		t.Errorf("提示應可直接複製執行: %q", hint)
	}
}

func TestVetArgsUsesTargets(t *testing.T) {
	got := strings.Join(vetArgs(defaultGoTargets), " ")
	if got != "vet ./..." {
		t.Errorf("得到 %q", got)
	}
}

func TestTestArgsAlwaysBypassesCache(t *testing.T) {
	// 少了 -count=1，第二次跑會直接回 cached，「通過」就成了上一次的結論。
	for _, args := range [][]string{
		testArgs([]string{"./..."}, "", false),
		testArgs([]string{"./..."}, "TestX", true),
	} {
		if !strings.Contains(strings.Join(args, " "), "-count=1") {
			t.Errorf("未帶 -count=1: %v", args)
		}
	}
	if got := strings.Join(testArgs([]string{"./..."}, "", false), " "); strings.Contains(got, "-run") || strings.Contains(got, "-v") {
		t.Errorf("未要求的旗標不應出現: %s", got)
	}
}

func TestWantGate(t *testing.T) {
	if !wantGate(nil, gateVet) {
		t.Error("清單留空應表示全跑")
	}
	if wantGate([]string{gateFmt}, gateVet) {
		t.Error("過濾後不應包含未選取的閘")
	}
	if !wantGate([]string{gateFmt, gateTest}, gateTest) {
		t.Error("已選取的閘應保留")
	}
}

// fakeToolchain 在臨時目錄裡放一支假的 go（以及可選的 gofmt），讓工具鏈解析不依賴本機 PATH。
func fakeToolchain(t *testing.T, withGofmt bool) string {
	t.Helper()
	dir := t.TempDir()
	writeExec(t, dir, "go.exe", "")
	if withGofmt {
		writeExec(t, dir, "gofmt.exe", "")
	}
	return filepath.Join(dir, "go.exe")
}

// writeExec 建立可執行檔形狀的檔案（Unix 上需要可執行位元，否則 LookPath 不認）。
func writeExec(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建立目錄失敗: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("建立 %s 失敗: %v", name, err)
	}
	return path
}

func TestGoCommandsRespectsGateFilter(t *testing.T) {
	goExe := fakeToolchain(t, true)

	t.Run("全跑時三道依固定順序", func(t *testing.T) {
		cmds, err := goCommands(invocation{goExe: goExe}, goExe)
		if err != nil {
			t.Fatalf("不應失敗: %v", err)
		}
		got := strings.Join(gateNamesOf(cmds), ",")
		if got != "Go 格式檢查,Go 靜態檢查,Go 測試" {
			t.Errorf("得到 %q", got)
		}
	})

	// 只要 vet 卻因為本機沒有 gofmt 而整個失敗，是把過濾條件實成了新的依賴。
	t.Run("未選 fmt 時不需要 gofmt", func(t *testing.T) {
		bare := fakeToolchain(t, false)
		cmds, err := goCommands(invocation{gates: []string{gateVet}}, bare)
		if err != nil {
			t.Fatalf("不應失敗: %v", err)
		}
		if len(cmds) != 1 || cmds[0].Exe != bare {
			t.Errorf("得到 %#v", cmds)
		}
	})

	t.Run("選了 fmt 而 gofmt 不在時點名 gofmt", func(t *testing.T) {
		bare := fakeToolchain(t, false)
		t.Setenv("PATH", t.TempDir()) // 讓 PATH 也查不到 gofmt
		_, err := goCommands(invocation{gates: []string{gateFmt}}, bare)
		if err == nil || !strings.Contains(err.Error(), "gofmt") {
			t.Fatalf("應回傳可判讀的錯誤，實際 %v", err)
		}
	})
}

// TestGoStepsMarkFormatGateAsReportOnly 釘住一個實測發現的缺陷：gofmt -l 即使列出未格式化
// 的檔案也回傳 0。少了這個標記，格式閘會永遠通過，而報表看起來和其他閘沒有兩樣。
func TestGoStepsMarkFormatGateAsReportOnly(t *testing.T) {
	root := fakeModule(t)
	goExe := fakeToolchain(t, true)

	fmtSteps, _, err := goSteps(invocation{repoRoot: root, goExe: goExe, gates: []string{gateFmt}})
	if err != nil {
		t.Fatalf("不應失敗: %v", err)
	}
	if len(fmtSteps) != 1 || !fmtSteps[0].failOnOutput {
		t.Errorf("格式閘必須標成「有輸出即未過」：%#v", fmtSteps)
	}
	if fmtSteps[0].hint == "" {
		t.Error("格式閘失敗時要給可複製的修正命令")
	}

	vetSteps, _, err := goSteps(invocation{repoRoot: root, goExe: goExe, gates: []string{gateVet}})
	if err != nil {
		t.Fatalf("不應失敗: %v", err)
	}
	if vetSteps[0].failOnOutput {
		t.Error("vet 以退出碼判定，有輸出（警告）不應直接判未過")
	}
}

func TestReportLines(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{name: "空字串", text: "", want: 0},
		{name: "只有換行與空白", text: "  \n\n\r\n\t\n", want: 0},
		{name: "一行檔案", text: "bad.go\n", want: 1},
		{name: "CRLF 多行", text: "a.go\r\nb.go\r\n", want: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reportLines(tc.text); len(got) != tc.want {
				t.Errorf("得到 %d 行（%v），預期 %d", len(got), got, tc.want)
			}
		})
	}
}
