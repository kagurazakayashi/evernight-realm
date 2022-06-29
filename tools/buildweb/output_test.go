package main

import (
	"path/filepath"
	"testing"
)

func TestResolveOutputConfinesToRepo(t *testing.T) {
	root, appDir := fakeRepo(t)
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
