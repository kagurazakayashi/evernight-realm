package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeDist 建立一份「看起來完整」的 Flutter Web 產物，回傳目錄。
//
// 內容照真實產物的樣式做最小複刻：必要檔案齊備，且 flutter_bootstrap.js 帶上
// useLocalCanvasKit 標記（缺它就代表產物會去向 CDN 取引擎）。
func writeDist(t *testing.T, marker bool) string {
	t.Helper()
	dir := t.TempDir()
	for _, rel := range requiredWebFiles {
		writeFile(t, dir, rel, "x")
	}
	bootstrap := "if(!e.useLocalCanvasKit){W(\"https://www.gstatic.com/flutter-canvaskit\")}\n"
	if marker {
		bootstrap += "window.FLUTTER_WEB_AUTO_BOOT=true;const e={\"useLocalCanvasKit\":true};\n"
	}
	writeFile(t, dir, "flutter_bootstrap.js", bootstrap)
	return dir
}

func TestVerifyArtifactsAcceptsCompleteOutput(t *testing.T) {
	dir := writeDist(t, true)

	report, err := verifyArtifacts(dir)
	if err != nil {
		t.Fatalf("完整產物不應失敗: %v", err)
	}
	if report.files < len(requiredWebFiles) {
		t.Errorf("檔案數 %d，少於必要清單 %d", report.files, len(requiredWebFiles))
	}
	if report.bytes <= 0 {
		t.Errorf("總位元組數應為正，得到 %d", report.bytes)
	}
}

func TestVerifyArtifactsRejectsIncompleteOutput(t *testing.T) {
	for _, missing := range requiredWebFiles {
		t.Run("缺少 "+missing, func(t *testing.T) {
			dir := writeDist(t, true)
			if err := os.Remove(filepath.Join(dir, filepath.FromSlash(missing))); err != nil {
				t.Fatalf("移除測試檔案失敗: %v", err)
			}
			_, err := verifyArtifacts(dir)
			if err == nil {
				t.Fatal("必要檔案缺失時應回傳錯誤")
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("錯誤應點出缺少的檔案 %s，實際: %v", missing, err)
			}
		})
	}
}

func TestVerifyArtifactsRejectsCDNCanvasKit(t *testing.T) {
	dir := writeDist(t, false)
	_, err := verifyArtifacts(dir)
	if err == nil {
		t.Fatal("產物未採用本機 CanvasKit 時應回傳錯誤，否則離線部署會白屏卻報成功")
	}
	if !strings.Contains(err.Error(), "CanvasKit") {
		t.Errorf("錯誤應說明 CanvasKit 來源問題: %v", err)
	}
}

func TestVerifyArtifactsOnEmptyDir(t *testing.T) {
	if _, err := verifyArtifacts(t.TempDir()); err == nil {
		t.Fatal("空目錄不應被當成可用產物")
	}
}

func TestRequiredWebFilesStayInsideOutput(t *testing.T) {
	for _, rel := range requiredWebFiles {
		if strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") {
			t.Errorf("必要清單的路徑寫法不安全: %q", rel)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		bytes int64
		want  string
	}{
		{bytes: 0, want: "0 B"},
		{bytes: 1023, want: "1023 B"},
		{bytes: 1024, want: "1.0 KiB"},
		{bytes: 78 * 1024 * 1024, want: "78.0 MiB"},
		{bytes: 3 * 1024 * 1024 * 1024, want: "3.0 GiB"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.bytes), func(t *testing.T) {
			if got := humanBytes(tc.bytes); got != tc.want {
				t.Errorf("humanBytes(%d) = %q，預期 %q", tc.bytes, got, tc.want)
			}
		})
	}
}
