package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubLookPath 在單一測試期間換掉可執行檔查證，結束時自動復原。
func stubLookPath(t *testing.T, stub func(string) (string, error)) {
	t.Helper()
	restore := lookupExecutable
	t.Cleanup(func() { lookupExecutable = restore })
	lookupExecutable = stub
}

// writeFile 在指定目錄建立檔案（含父目錄），回傳路徑。
func writeFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建立目錄失敗: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("寫入 %s 失敗: %v", rel, err)
	}
	return path
}

// fakeRepo 建立一個最小的假倉庫：有 .gitmodules、子模組目錄與 pubspec.yaml。
// 回傳（倉庫根, 子模組目錄）。
func fakeRepo(t *testing.T, gitmodules string) (string, string) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, gitmodulesFile, gitmodules)
	appDir := filepath.Join(root, "frontend-app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("建立子模組目錄失敗: %v", err)
	}
	writeFile(t, appDir, pubspecFile, "name: frontend_app\n")
	return root, appDir
}

const realGitmodules = `[submodule "evernight-realm-app"]
	path = evernight-realm-app
	url = git@github.com:kagurazakayashi/evernight-realm-app.git
`

func TestParseGitmodules(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []submoduleEntry
	}{
		{
			name:    "真實格式",
			content: realGitmodules,
			want:    []submoduleEntry{{name: "evernight-realm-app", path: "evernight-realm-app"}},
		},
		{
			name: "多筆依順序回傳",
			content: `[submodule "a"]
	path = dir-a
[submodule "b"]
	path = libs/dir-b
`,
			want: []submoduleEntry{{name: "a", path: "dir-a"}, {name: "b", path: "libs/dir-b"}},
		},
		{
			name:    "CRLF 與註解與多餘空白",
			content: "; 註解\r\n# 註解\r\n  [submodule \"a\"]\r\n\tpath  =  dir-a \r\n branch = main\r\n",
			want:    []submoduleEntry{{name: "a", path: "dir-a"}},
		},
		{
			name:    "path 帶引號",
			content: "[submodule \"a\"]\npath = \"dir a\"\n",
			want:    []submoduleEntry{{name: "a", path: "dir a"}},
		},
		{
			name:    "缺少 path 的記錄不成立",
			content: "[submodule \"a\"]\nurl = x\n",
			want:    nil,
		},
		{
			name:    "區段外的鍵值被忽略",
			content: "path = nope\n[submodule \"a\"]\npath = ok-dir\n",
			want:    []submoduleEntry{{name: "a", path: "ok-dir"}},
		},
		{
			name:    "空檔",
			content: "",
			want:    nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseGitmodules(tc.content)
			if len(got) != len(tc.want) {
				t.Fatalf("筆數 %d，預期 %d（%#v）", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("第 %d 筆為 %#v，預期 %#v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestReadSubmodulesRejectsMissingFile(t *testing.T) {
	if _, err := readSubmodules(t.TempDir()); err == nil {
		t.Fatal("缺少 .gitmodules 時應回傳錯誤")
	}
}

func TestReadSubmodulesRejectsEmptyList(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, gitmodulesFile, "# 尚無子模組\n")
	if _, err := readSubmodules(root); err == nil {
		t.Fatal(".gitmodules 內無記錄時應回傳錯誤")
	}
}

func TestSelectSubmodule(t *testing.T) {
	single := []submoduleEntry{{name: "app", path: "frontend-app"}}
	multi := []submoduleEntry{{name: "app", path: "frontend-app"}, {name: "docs", path: "docs-site"}}

	t.Run("恰好一筆時不需指名", func(t *testing.T) {
		got, err := selectSubmodule(single, "")
		if err != nil {
			t.Fatalf("不應失敗: %v", err)
		}
		if got.name != "app" {
			t.Errorf("選到 %q", got.name)
		}
	})

	t.Run("多筆而未指名時拒絕並列出選項", func(t *testing.T) {
		_, err := selectSubmodule(multi, "")
		if err == nil {
			t.Fatal("多筆子模組未指名時應拒絕")
		}
		if !strings.Contains(err.Error(), "--module") {
			t.Errorf("錯誤訊息應指出可用 --module 指名: %v", err)
		}
		if !strings.Contains(err.Error(), "docs") {
			t.Errorf("錯誤訊息應列出現有記錄: %v", err)
		}
	})

	t.Run("依名稱或路徑指名", func(t *testing.T) {
		for _, wanted := range []string{"docs", "docs-site"} {
			got, err := selectSubmodule(multi, wanted)
			if err != nil {
				t.Fatalf("以 %q 指名失敗: %v", wanted, err)
			}
			if got.path != "docs-site" {
				t.Errorf("以 %q 指名選到 %#v", wanted, got)
			}
		}
	})

	t.Run("名稱大小寫不敏感", func(t *testing.T) {
		got, err := selectSubmodule(multi, "APP")
		if err != nil || got.path != "frontend-app" {
			t.Fatalf("大小寫應不敏感，實際 err=%v got=%#v", err, got)
		}
	})

	t.Run("指名對不上時不退回預設", func(t *testing.T) {
		if _, err := selectSubmodule(single, "nope"); err == nil {
			t.Fatal("指名不存在時應拒絕，而不是拿唯一的記錄頂替")
		}
	})
}

func TestResolveRepoRootFrom(t *testing.T) {
	root, appDir := fakeRepo(t, realGitmodules)

	t.Run("自子目錄向上找到倉庫根", func(t *testing.T) {
		nested := filepath.Join(appDir, "lib", "src")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatalf("建立深層目錄失敗: %v", err)
		}
		got, err := resolveRepoRootFrom(nested, "")
		if err != nil {
			t.Fatalf("不應失敗: %v", err)
		}
		if filepath.Clean(got) != filepath.Clean(root) {
			t.Errorf("找到 %q，預期 %q", got, root)
		}
	})

	t.Run("明指倉庫根", func(t *testing.T) {
		got, err := resolveRepoRootFrom(t.TempDir(), root)
		if err != nil {
			t.Fatalf("不應失敗: %v", err)
		}
		if filepath.Clean(got) != filepath.Clean(root) {
			t.Errorf("得到 %q", got)
		}
	})

	t.Run("明指的目錄不存在時拒絕", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope")
		if _, err := resolveRepoRootFrom(t.TempDir(), missing); err == nil {
			t.Fatal("目錄不存在時應拒絕")
		}
	})

	t.Run("明指的目錄內無 .gitmodules 時拒絕", func(t *testing.T) {
		if _, err := resolveRepoRootFrom(t.TempDir(), appDir); err == nil {
			t.Fatal("子模組目錄不是倉庫根，應拒絕")
		}
	})

	t.Run("向上找不到時拒絕且指明起點", func(t *testing.T) {
		orphan := t.TempDir()
		_, err := resolveRepoRootFrom(orphan, "")
		if err == nil {
			t.Fatal("找不到 .gitmodules 時應回傳錯誤")
		}
		if !strings.Contains(err.Error(), gitmodulesFile) {
			t.Errorf("錯誤訊息應點出找不到的檔案: %v", err)
		}
	})
}

func TestSubmoduleDirStaysInsideRoot(t *testing.T) {
	root, _ := fakeRepo(t, realGitmodules)

	t.Run("正常相對路徑", func(t *testing.T) {
		got, err := submoduleDir(root, submoduleEntry{name: "app", path: "frontend-app"})
		if err != nil {
			t.Fatalf("不應失敗: %v", err)
		}
		if filepath.Clean(got) != filepath.Join(filepath.Clean(root), "frontend-app") {
			t.Errorf("得到 %q", got)
		}
	})

	rejects := []struct {
		name string
		path string
	}{
		{name: "上跳一層", path: "../elsewhere"},
		{name: "中途上跳", path: "libs/../../elsewhere"},
		{name: "指向倉庫根", path: "."},
		{name: "空白", path: "   "},
	}
	for _, tc := range rejects {
		t.Run("拒絕 "+tc.name, func(t *testing.T) {
			if _, err := submoduleDir(root, submoduleEntry{name: "app", path: tc.path}); err == nil {
				t.Errorf("path=%q 應被拒絕", tc.path)
			}
		})
	}
}

func TestPreflightClassifiesFailure(t *testing.T) {
	const fakeFlutter = "flutter"

	// 前置檢查只驗「存在且可用」，不碰 git；這裡把可執行檔查證換成可控的回傳，
	// 讓每個子測試只針對一個目錄狀態。
	stubLookPath(t, func(string) (string, error) { return fakeFlutter, nil })

	t.Run("目錄不存在", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, gitmodulesFile, realGitmodules)
		_, err := preflight(root, submoduleEntry{name: "app", path: "frontend-app"}, "")
		if !errors.Is(err, ErrSubmoduleMissing) {
			t.Fatalf("應為 ErrSubmoduleMissing，實際 %v", err)
		}
		if !strings.Contains(err.Error(), "不代下載") {
			t.Errorf("錯誤訊息應說明不做下載: %v", err)
		}
	})

	t.Run("目錄存在但為空", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, gitmodulesFile, realGitmodules)
		if err := os.MkdirAll(filepath.Join(root, "frontend-app"), 0o755); err != nil {
			t.Fatalf("建立目錄失敗: %v", err)
		}
		_, err := preflight(root, submoduleEntry{name: "app", path: "frontend-app"}, "")
		if !errors.Is(err, ErrSubmoduleMissing) {
			t.Fatalf("應為 ErrSubmoduleMissing，實際 %v", err)
		}
		if !strings.Contains(err.Error(), "空目錄") {
			t.Errorf("空目錄與不存在應可從訊息分辨: %v", err)
		}
	})

	t.Run("目錄存在但不是 Flutter 專案", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, gitmodulesFile, realGitmodules)
		writeFile(t, root, "frontend-app/README.md", "其他東西\n")
		_, err := preflight(root, submoduleEntry{name: "app", path: "frontend-app"}, "")
		if !errors.Is(err, ErrSubmoduleMissing) {
			t.Fatalf("應為 ErrSubmoduleMissing，實際 %v", err)
		}
		if !strings.Contains(err.Error(), pubspecFile) {
			t.Errorf("錯誤訊息應點出缺少的檔案: %v", err)
		}
	})

	t.Run("flutter 找不到時回報查過的候選", func(t *testing.T) {
		root, _ := fakeRepo(t, realGitmodules)
		t.Setenv("FLUTTER_ROOT", "")
		stubLookPath(t, func(string) (string, error) { return "", errors.New("查無此檔") })
		_, err := preflight(root, submoduleEntry{name: "app", path: "frontend-app"}, "")
		if err == nil {
			t.Fatal("找不到 flutter 時應回傳錯誤")
		}
		if !strings.Contains(err.Error(), "--flutter") {
			t.Errorf("錯誤訊息應指出可用 --flutter 指定: %v", err)
		}
	})

	t.Run("全部就緒", func(t *testing.T) {
		root, appDir := fakeRepo(t, realGitmodules)
		got, err := preflight(root, submoduleEntry{name: "app", path: "frontend-app"}, "")
		if err != nil {
			t.Fatalf("不應失敗: %v", err)
		}
		if filepath.Clean(got.appDir) != filepath.Clean(appDir) {
			t.Errorf("appDir=%q，預期 %q", got.appDir, appDir)
		}
		if got.flutterExe != fakeFlutter {
			t.Errorf("flutterExe=%q", got.flutterExe)
		}
		if got.module != "app" {
			t.Errorf("module=%q", got.module)
		}
	})
}

func TestPlanFlutterLookupOrder(t *testing.T) {
	t.Run("指名時只看指名", func(t *testing.T) {
		got := planFlutterLookup("D:/SDK/flutter/bin/flutter.bat", "D:/OtherSDK/flutter")
		if len(got) != 1 || got[0] != "D:/SDK/flutter/bin/flutter.bat" {
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

func TestResolveOutputConfinesToRepo(t *testing.T) {
	root, appDir := fakeRepo(t, realGitmodules)
	nested := filepath.Join(root, "internal", "webassets", "dist")

	t.Run("預設相對路徑以倉庫根為基準", func(t *testing.T) {
		got, err := resolveOutput(root, defaultOutputRel, appDir)
		if err != nil {
			t.Fatalf("不應失敗: %v", err)
		}
		if filepath.Clean(got) != filepath.Clean(nested) {
			t.Errorf("得到 %q，預期 %q", got, nested)
		}
	})

	t.Run("絕對路徑原樣採用", func(t *testing.T) {
		got, err := resolveOutput(root, nested, appDir)
		if err != nil || filepath.Clean(got) != filepath.Clean(nested) {
			t.Fatalf("err=%v got=%q", err, got)
		}
	})

	t.Run("斜線寫法與反斜線寫法同結果", func(t *testing.T) {
		a, errA := resolveOutput(root, "internal/webassets/dist", appDir)
		b, errB := resolveOutput(root, filepath.Join("internal", "webassets", "dist"), appDir)
		if errA != nil || errB != nil {
			t.Fatalf("errA=%v errB=%v", errA, errB)
		}
		if filepath.Clean(a) != filepath.Clean(b) {
			t.Errorf("兩種寫法結果不同: %q vs %q", a, b)
		}
	})

	rejects := []struct {
		name  string
		value string
	}{
		{name: "倉庫根之外", value: filepath.Join(filepath.Dir(root), "outside")},
		{name: "以 .. 上跳", value: "../sibling"},
		{name: "倉庫根本身", value: "."},
		{name: "空白", value: "   "},
	}
	for _, tc := range rejects {
		t.Run("拒絕 "+tc.name, func(t *testing.T) {
			if _, err := resolveOutput(root, tc.value, appDir); err == nil {
				t.Errorf("output=%q 應被拒絕", tc.value)
			}
		})
	}

	t.Run("拒絕包住子模組的目錄", func(t *testing.T) {
		if _, err := resolveOutput(root, filepath.Dir(appDir), appDir); err == nil {
			t.Error("產物目錄包住子模組時應拒絕")
		}
	})

	t.Run("拒絕落在子模組之內", func(t *testing.T) {
		if _, err := resolveOutput(root, "frontend-app/build/web", appDir); err == nil {
			t.Error("產物目錄在子模組內時應拒絕（清空動作會動到前端）")
		}
	})
}
