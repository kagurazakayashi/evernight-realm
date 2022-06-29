package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuildArgs(t *testing.T) {
	const out = `D:\repo\internal\webassets\dist`

	t.Run("預設為 release 且一律關閉線上 CDN 資源", func(t *testing.T) {
		got := buildArgs(out, options{})
		joined := strings.Join(got, " ")
		if !strings.HasPrefix(joined, "build web --release "+noWebResourcesCDN) {
			t.Fatalf("參數順序不符預期: %s", joined)
		}
		if !strings.Contains(joined, "-o "+out) {
			t.Errorf("未帶輸出目錄: %s", joined)
		}
	})

	t.Run("debug 只換模式不換其他參數", func(t *testing.T) {
		got := strings.Join(buildArgs(out, options{debug: true}), " ")
		if !strings.Contains(got, " --debug ") || strings.Contains(got, "--release") {
			t.Errorf("debug 建置參數錯誤: %s", got)
		}
		if !strings.Contains(got, noWebResourcesCDN) {
			t.Errorf("debug 建置也要關閉 CDN 資源: %s", got)
		}
	})

	t.Run("額外編譯期參數逐筆附帶", func(t *testing.T) {
		o := options{defines: stringList{
			"ER_SERVER_BASE_URL=http://127.0.0.1:5299",
			"ER_BUILD_VERSION=0.1.0-dev",
		}}
		got := buildArgs(out, o)
		want := []string{"--dart-define=ER_SERVER_BASE_URL=http://127.0.0.1:5299", "--dart-define=ER_BUILD_VERSION=0.1.0-dev"}
		if len(got) < len(want) {
			t.Fatalf("參數太少: %v", got)
		}
		tail := strings.Join(got[len(got)-len(want):], " ")
		if tail != strings.Join(want, " ") {
			t.Errorf("尾端參數為 %q，預期 %q", tail, strings.Join(want, " "))
		}
	})

	t.Run("建置引數不含任何 git 命令", func(t *testing.T) {
		for _, arg := range buildArgs(out, options{defines: stringList{"A=B"}}) {
			if arg == "git" || strings.Contains(arg, "submodule") {
				t.Errorf("建置工具不得執行 git 操作，卻出現引數 %q", arg)
			}
		}
	})
}

func TestQualityCommandsShape(t *testing.T) {
	gates, err := qualityCommands(`D:\SDK\flutter\bin\flutter.bat`)
	if err != nil {
		t.Fatalf("組裝品質閘失敗: %v", err)
	}
	if len(gates) != 3 {
		t.Fatalf("應為三道品質閘，得到 %d", len(gates))
	}

	const dartExe = `D:\SDK\flutter\bin\dart.bat`
	if gates[0].exe != dartExe {
		t.Errorf("第一道應使用同目錄 dart，得到 %q", gates[0].exe)
	}
	if got := strings.Join(gates[0].args, " "); !strings.Contains(got, "--set-exit-if-changed") {
		t.Errorf("格式閘未設定差異即失敗: %s", got)
	}
	for _, gate := range gates[1:] {
		if gate.exe != `D:\SDK\flutter\bin\flutter.bat` {
			t.Errorf("%s 應使用 flutter 本身，得到 %q", gate.desc, gate.exe)
		}
	}

	t.Run("說明文字不複寫引數值", func(t *testing.T) {
		for _, gate := range gates {
			if strings.Contains(gate.desc, "--") {
				t.Errorf("desc 不應出現引數: %q", gate.desc)
			}
		}
	})
}

func TestDartAlongside(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "Windows 批次檔", input: `D:\SDK\flutter\bin\flutter.bat`, want: `D:\SDK\flutter\bin\dart.bat`},
		{name: "Windows 執行檔", input: `C:\f\futter\flutter.exe`, want: `C:\f\futter\dart.exe`},
		{name: "Unix 無副檔名", input: "/opt/flutter/bin/flutter", want: "/opt/flutter/bin/dart"},
		{name: "大寫開頭", input: `D:\f\Flutter.bat`, want: `D:\f\Dart.bat`},
		{name: "檔名不是 flutter 開頭時不猜", input: "/opt/fui", want: ""},
		{name: "只有檔名", input: "flutter", want: "dart"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dartAlongside(tc.input); got != tc.want {
				t.Errorf("dartAlongside(%q) = %q，預期 %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestDartExecutable(t *testing.T) {
	dir := t.TempDir()

	t.Run("同目錄的 dart 優先", func(t *testing.T) {
		writeFile(t, dir, "dart.bat", "@echo off\n")
		writeFile(t, dir, "flutter.bat", "@echo off\n")
		got, err := dartExecutable(filepath.Join(dir, "flutter.bat"))
		if err != nil {
			t.Fatalf("不應失敗: %v", err)
		}
		if filepath.Clean(got) != filepath.Clean(filepath.Join(dir, "dart.bat")) {
			t.Errorf("得到 %q", got)
		}
	})

	t.Run("同目錄推不出來時退回 PATH", func(t *testing.T) {
		stubLookPath(t, func(name string) (string, error) {
			if name != "dart" {
				return "", errors.New("只應查 dart")
			}
			return `/other/sdk/dart`, nil
		})
		got, err := dartExecutable("/nowhere/FlutterTool")
		if err != nil {
			t.Fatalf("不應失敗: %v", err)
		}
		if got != "/other/sdk/dart" {
			t.Errorf("得到 %q", got)
		}
	})

	t.Run("都找不到時回傳錯誤並列出試過的路徑", func(t *testing.T) {
		stubLookPath(t, func(string) (string, error) { return "", errors.New("查無") })
		missing := filepath.Join(t.TempDir(), "flutter.bat")
		_, err := dartExecutable(missing)
		if err == nil {
			t.Fatal("找不到 dart 時應回傳錯誤")
		}
		if !strings.Contains(err.Error(), "dart") {
			t.Errorf("錯誤訊息應點出 dart: %v", err)
		}
	})
}

func TestFlutterCommandRun(t *testing.T) {
	ctx := context.Background()

	t.Run("命令不存在時錯誤可判讀", func(t *testing.T) {
		cmd := flutterCommand{desc: "不存在的命令", exe: "buildweb-no-such-executable-xyz"}
		err := cmd.run(ctx, t.TempDir(), os.Stdout, os.Stderr)
		if err == nil {
			t.Fatal("應回傳錯誤")
		}
		if !strings.Contains(err.Error(), "不存在的命令") {
			t.Errorf("錯誤應含說明文字: %v", err)
		}
	})

	t.Run("非零退出碼寫進錯誤訊息", func(t *testing.T) {
		var cmd flutterCommand
		if runtime.GOOS == "windows" {
			shell := os.Getenv("ComSpec")
			if shell == "" {
				shell = "cmd.exe"
			}
			cmd = flutterCommand{desc: "回傳 3", exe: shell, args: []string{"/c", "exit", "3"}}
		} else {
			cmd = flutterCommand{desc: "回傳 3", exe: "/bin/sh", args: []string{"-c", "exit 3"}}
		}
		err := cmd.run(ctx, t.TempDir(), os.Stdout, os.Stderr)
		if err == nil {
			t.Fatal("非零退出碼應回傳錯誤")
		}
		if !strings.Contains(err.Error(), "退出碼 3") {
			t.Errorf("錯誤應含退出碼: %v", err)
		}
	})

	t.Run("成功時輸出轉接給呼叫方", func(t *testing.T) {
		var stdout, stderr strings.Builder
		var cmd flutterCommand
		if runtime.GOOS == "windows" {
			shell := os.Getenv("ComSpec")
			if shell == "" {
				shell = "cmd.exe"
			}
			cmd = flutterCommand{desc: "印一行", exe: shell, args: []string{"/c", "echo buildweb-probe"}}
		} else {
			cmd = flutterCommand{desc: "印一行", exe: "/bin/sh", args: []string{"-c", "echo buildweb-probe"}}
		}
		if err := cmd.run(context.Background(), t.TempDir(), &stdout, &stderr); err != nil {
			t.Fatalf("不應失敗: %v", err)
		}
		if !strings.Contains(stdout.String(), "buildweb-probe") {
			t.Errorf("輸出未轉接: %q", stdout.String())
		}
	})
}

func TestCleanOutput(t *testing.T) {
	t.Run("目錄不存在時不報錯", func(t *testing.T) {
		if err := cleanOutput(filepath.Join(t.TempDir(), "nope"), &strings.Builder{}); err != nil {
			t.Fatalf("不應失敗: %v", err)
		}
	})

	t.Run("清空既有產物", func(t *testing.T) {
		dir := t.TempDir()
		out := filepath.Join(dir, "dist")
		writeFile(t, out, "main.dart.js", "舊產物")
		var stdout strings.Builder
		if err := cleanOutput(out, &stdout); err != nil {
			t.Fatalf("不應失敗: %v", err)
		}
		if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("產物目錄應已被移除，實際 err=%v", err)
		}
		if !strings.Contains(stdout.String(), "已清空") {
			t.Errorf("應留下清空紀錄: %q", stdout.String())
		}
	})

	t.Run("目標是檔案時拒絕", func(t *testing.T) {
		dir := t.TempDir()
		path := writeFile(t, dir, "dist", "這不是一個目錄")
		if err := cleanOutput(path, &strings.Builder{}); err == nil {
			t.Fatal("產物路徑為檔案時應拒絕，避免拿刪除目錄的邏輯去動檔案")
		}
		if !fileExists(path) {
			t.Error("拒絕時不得順手刪掉該檔案")
		}
	})
}
