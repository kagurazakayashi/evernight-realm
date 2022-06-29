package devkit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareFrontendClassifiesFailure(t *testing.T) {
	const fakeFlutter = "flutter"

	// 前置檢查只驗「存在且可用」，不碰 git；這裡把可執行檔查證換成可控的回傳，
	// 讓每個子測試只針對一個目錄狀態。
	stubLookPath(t, func(string) (string, error) { return fakeFlutter, nil })

	t.Run("目錄不存在", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, GitmodulesFile, realGitmodules)
		_, err := PrepareFrontend(root, SubmoduleEntry{Name: "app", Path: "frontend-app"}, "")
		if !errors.Is(err, ErrSubmoduleMissing) {
			t.Fatalf("應為 ErrSubmoduleMissing，實際 %v", err)
		}
		if !strings.Contains(err.Error(), "不代下載") {
			t.Errorf("錯誤訊息應說明不做下載: %v", err)
		}
	})

	t.Run("目錄存在但為空", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, GitmodulesFile, realGitmodules)
		if err := os.MkdirAll(filepath.Join(root, "frontend-app"), 0o755); err != nil {
			t.Fatalf("建立目錄失敗: %v", err)
		}
		_, err := PrepareFrontend(root, SubmoduleEntry{Name: "app", Path: "frontend-app"}, "")
		if !errors.Is(err, ErrSubmoduleMissing) {
			t.Fatalf("應為 ErrSubmoduleMissing，實際 %v", err)
		}
		if !strings.Contains(err.Error(), "空目錄") {
			t.Errorf("空目錄與不存在應可從訊息分辨: %v", err)
		}
	})

	t.Run("目錄存在但不是 Flutter 專案", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, GitmodulesFile, realGitmodules)
		writeFile(t, root, "frontend-app/README.md", "其他東西\n")
		_, err := PrepareFrontend(root, SubmoduleEntry{Name: "app", Path: "frontend-app"}, "")
		if !errors.Is(err, ErrSubmoduleMissing) {
			t.Fatalf("應為 ErrSubmoduleMissing，實際 %v", err)
		}
		if !strings.Contains(err.Error(), PubspecFile) {
			t.Errorf("錯誤訊息應點出缺少的檔案: %v", err)
		}
	})

	t.Run("flutter 找不到時回報查過的候選", func(t *testing.T) {
		root, _ := fakeRepo(t, realGitmodules)
		t.Setenv("FLUTTER_ROOT", "")
		stubLookPath(t, func(string) (string, error) { return "", errors.New("查無此檔") })
		_, err := PrepareFrontend(root, SubmoduleEntry{Name: "app", Path: "frontend-app"}, "")
		if err == nil {
			t.Fatal("找不到 flutter 時應回傳錯誤")
		}
		if !strings.Contains(err.Error(), "--flutter") {
			t.Errorf("錯誤訊息應指出可用 --flutter 指定: %v", err)
		}
	})

	t.Run("全部就緒", func(t *testing.T) {
		root, appDir := fakeRepo(t, realGitmodules)
		got, err := PrepareFrontend(root, SubmoduleEntry{Name: "app", Path: "frontend-app"}, "")
		if err != nil {
			t.Fatalf("不應失敗: %v", err)
		}
		if filepath.Clean(got.Dir) != filepath.Clean(appDir) {
			t.Errorf("Dir=%q，預期 %q", got.Dir, appDir)
		}
		if got.FlutterExe != fakeFlutter {
			t.Errorf("FlutterExe=%q", got.FlutterExe)
		}
		if got.Module != "app" {
			t.Errorf("Module=%q", got.Module)
		}
	})
}

func TestPlanFlutterLookupOrder(t *testing.T) {
	t.Run("指名時只看指名", func(t *testing.T) {
		got := planFlutterLookup(`D:\SDK\flutter\bin\flutter.bat`, `D:\OtherSDK\flutter`)
		if len(got) != 1 || got[0] != `D:\SDK\flutter\bin\flutter.bat` {
			t.Errorf("得到 %#v", got)
		}
	})

	t.Run("未指名時先 PATH 再 FLUTTER_ROOT", func(t *testing.T) {
		got := planFlutterLookup("", "/opt/flutter")
		if len(got) != 2 || got[0] != "flutter" {
			t.Fatalf("得到 %#v", got)
		}
		if filepath.Clean(got[1]) != filepath.Clean("/opt/flutter/bin/flutter") {
			t.Errorf("備援路徑為 %q", got[1])
		}
	})

	t.Run("FLUTTER_ROOT 未設時只有 PATH", func(t *testing.T) {
		got := planFlutterLookup("", "   ")
		if len(got) != 1 || got[0] != "flutter" {
			t.Errorf("得到 %#v", got)
		}
	})
}

func TestPlanGoLookup(t *testing.T) {
	t.Run("指名時只看指名", func(t *testing.T) {
		got := planGoLookup(`/custom/bin/go`, `/usr/local/go`)
		if len(got) != 1 || got[0] != `/custom/bin/go` {
			t.Errorf("得到 %#v", got)
		}
	})

	t.Run("未指名時先 PATH 再 GOROOT", func(t *testing.T) {
		got := planGoLookup("", "/usr/local/go")
		if len(got) != 2 || got[0] != "go" {
			t.Fatalf("得到 %#v", got)
		}
		if filepath.Clean(got[1]) != filepath.Clean("/usr/local/go/bin/go") {
			t.Errorf("備援路徑為 %q", got[1])
		}
	})

	t.Run("GOROOT 未設時只有 PATH", func(t *testing.T) {
		got := planGoLookup("", "")
		if len(got) != 1 || got[0] != "go" {
			t.Errorf("得到 %#v", got)
		}
	})
}

func TestResolveGoUsesGivenCandidate(t *testing.T) {
	stubLookPath(t, func(name string) (string, error) {
		if name != `/somewhere/go` {
			return "", errors.New("指名時不應查其他候選")
		}
		return name, nil
	})
	got, err := ResolveGo(`/somewhere/go`)
	if err != nil || got != `/somewhere/go` {
		t.Fatalf("err=%v got=%q", err, got)
	}
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

func TestGofmtAlongside(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "Windows go.exe", input: `D:\SDK\go\bin\go.exe`, want: `D:\SDK\go\bin\gofmt.exe`},
		{name: "Unix go", input: "/usr/local/go/bin/go", want: "/usr/local/go/bin/gofmt"},
		{name: "只有檔名", input: "go", want: "gofmt"},
		{name: "版本化檔名仍取同目錄", input: `D:\go\bin\go1.27.1.exe`, want: `D:\go\bin\gofmt.exe`},
		{name: "空路徑不猜", input: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gofmtAlongside(tc.input); got != tc.want {
				t.Errorf("gofmtAlongside(%q) = %q，預期 %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestDartExecutable(t *testing.T) {
	dir := t.TempDir()

	t.Run("同目錄的 dart 優先", func(t *testing.T) {
		writeFile(t, dir, "dart.bat", "@echo off\n")
		writeFile(t, dir, "flutter.bat", "@echo off\n")
		got, err := DartExecutable(filepath.Join(dir, "flutter.bat"))
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
		got, err := DartExecutable("/nowhere/FlutterTool")
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
		_, err := DartExecutable(missing)
		if err == nil {
			t.Fatal("找不到 dart 時應回傳錯誤")
		}
		if !strings.Contains(err.Error(), "dart") {
			t.Errorf("錯誤訊息應點出 dart: %v", err)
		}
	})
}

func TestGoFmtExecutable(t *testing.T) {
	dir := t.TempDir()

	t.Run("同目錄的 gofmt 優先", func(t *testing.T) {
		path := writeFile(t, dir, "gofmt.exe", "")
		got, err := GoFmtExecutable(filepath.Join(dir, "go.exe"))
		if err != nil {
			t.Fatalf("不應失敗: %v", err)
		}
		if filepath.Clean(got) != filepath.Clean(path) {
			t.Errorf("得到 %q", got)
		}
	})

	t.Run("同目錄沒有時退回 PATH", func(t *testing.T) {
		stubLookPath(t, func(name string) (string, error) {
			if name != "gofmt" {
				return "", errors.New("只應查 gofmt")
			}
			return `/other/gofmt`, nil
		})
		got, err := GoFmtExecutable(filepath.Join(t.TempDir(), "go.exe"))
		if err != nil || got != `/other/gofmt` {
			t.Fatalf("err=%v got=%q", err, got)
		}
	})

	t.Run("都找不到時點出 gofmt", func(t *testing.T) {
		stubLookPath(t, func(string) (string, error) { return "", errors.New("查無") })
		_, err := GoFmtExecutable(filepath.Join(t.TempDir(), "go.exe"))
		if err == nil || !strings.Contains(err.Error(), "gofmt") {
			t.Fatalf("應回傳可判讀的錯誤，實際 %v", err)
		}
	})
}
