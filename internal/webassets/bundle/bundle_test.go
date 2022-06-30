package bundle

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// completeBundle 組出一份通過判定的最小產物檔案樹（內容以「x」佔位，只在意存在與否）。
//
// absent 用於反向測試：把清單裡某個檔案拿掉，檢查是否點出的正是那一個。
func completeBundle(absent string) fstest.MapFS {
	files := map[string]string{"extra/README": "多一個檔案不該影響判定"}
	for _, rel := range Required() {
		files[rel] = "x"
	}
	delete(files, absent)

	fsys := fstest.MapFS{}
	for name, content := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(content)}
	}
	if absent != BootstrapFile {
		fsys[BootstrapFile] = &fstest.MapFile{Data: []byte("const e={" + LocalCanvasKitMarker + "};")}
	}
	return fsys
}

func TestVerifyAcceptsCompleteBundle(t *testing.T) {
	fsys := completeBundle("")
	fsys[PlaceholderName] = &fstest.MapFile{Data: []byte(PlaceholderNote)}

	report, err := Verify(fsys)
	if err != nil {
		t.Fatalf("完整產物不應失敗: %v", err)
	}
	if report.Files != len(Required())+1 {
		t.Errorf("檔案數應為必要清單 %d 加一個額外檔案，實際 %d", len(Required()), report.Files)
	}
	if report.Bytes <= 0 {
		t.Errorf("總位元組數應為正，實際 %d", report.Bytes)
	}
}

func TestVerifyRejectsEachMissingRequiredFile(t *testing.T) {
	for _, rel := range Required() {
		t.Run("缺少 "+rel, func(t *testing.T) {
			_, err := Verify(completeBundle(rel))
			var missing *MissingFileError
			if !errors.As(err, &missing) {
				t.Fatalf("必要檔案缺失時應回 MissingFileError，實際 %v", err)
			}
			if missing.Path != rel {
				t.Errorf("應點出 %s，實際 %s", rel, missing.Path)
			}
			if !strings.Contains(err.Error(), rel) {
				t.Errorf("錯誤訊息應含檔案路徑: %v", err)
			}
		})
	}
}

func TestVerifyRejectsEmptyTree(t *testing.T) {
	_, err := Verify(fstest.MapFS{})
	var missing *MissingFileError
	if !errors.As(err, &missing) {
		t.Fatalf("空檔案樹不應被當成可用產物，實際 %v", err)
	}
	// 空樹的第一個症狀一定是清單第一筆；點名它，人才會去查是不是還沒建置前端。
	if missing.Path != Required()[0] {
		t.Errorf("空樹應點出第一個必要檔案 %s，實際 %s", Required()[0], missing.Path)
	}
}

func TestVerifyRejectsRequiredPathAsDirectory(t *testing.T) {
	// 與必要檔案同名的目錄不是「檔案存在」：以真實檔案系統重現，避免測試替身自己圓自己的說法。
	dir := t.TempDir()
	for name, file := range completeBundle("") {
		if name == "index.html" {
			continue
		}
		writeBundleFile(t, dir, name, file.Data)
	}
	if err := os.MkdirAll(filepath.Join(dir, "index.html"), 0o755); err != nil {
		t.Fatalf("建立同名目錄失敗: %v", err)
	}

	_, err := Verify(os.DirFS(dir))
	var missing *MissingFileError
	if !errors.As(err, &missing) {
		t.Fatalf("必要檔案是目錄時應回 MissingFileError，實際 %v", err)
	}
	if missing.Path != "index.html" {
		t.Errorf("應點出 index.html，實際 %s", missing.Path)
	}
}

// writeBundleFile 在 dir 下按斜線路徑建立檔案。
func writeBundleFile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建立目錄 %s 失敗: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("寫入 %s 失敗: %v", path, err)
	}
}

func TestVerifyRejectsCDNCanvasKit(t *testing.T) {
	fsys := completeBundle("")
	fsys[BootstrapFile] = &fstest.MapFile{Data: []byte(`{"useLocalCanvasKit":false}`)}

	_, err := Verify(fsys)
	if !errors.Is(err, ErrNoLocalCanvasKit) {
		t.Fatalf("產物未採用本機 CanvasKit 時應回 ErrNoLocalCanvasKit，實際 %v", err)
	}
	if !strings.Contains(err.Error(), "CanvasKit") {
		t.Errorf("錯誤應說明 CanvasKit 來源問題: %v", err)
	}
}

func TestVerifyReportsReadFailureAsReadFailure(t *testing.T) {
	// 讀不到外殼檔案時不能回報「缺少必要檔案」：檔案在、讀不了，兩者的下一步動作不同。
	fsys := readErrorFS{completeBundle("")}
	if _, err := Verify(fsys); err == nil {
		t.Fatal("讀取失敗時不應判定通過")
	} else if strings.Contains(err.Error(), "缺少必要檔案") {
		t.Errorf("讀取失敗不應改寫成缺失，實際 %v", err)
	}
}

func TestPlaceholderDoesNotChangeStatistics(t *testing.T) {
	// 佔位檔會跟著產物一起內嵌進執行檔；若計入統計，啟動摘要的檔案數會比建置工具報的多一個。
	withPlaceholder := completeBundle("")
	withPlaceholder[PlaceholderName] = &fstest.MapFile{Data: []byte(PlaceholderNote)}

	before, errBefore := Verify(completeBundle(""))
	after, errAfter := Verify(withPlaceholder)
	if errBefore != nil || errAfter != nil {
		t.Fatalf("兩者都該通過: %v / %v", errBefore, errAfter)
	}
	if before != after {
		t.Errorf("佔位檔不應改變統計: %v → %v", before, after)
	}
}

func TestRequiredReturnsCopy(t *testing.T) {
	first := Required()
	if len(first) == 0 {
		t.Fatal("必要清單不應為空")
	}
	total := len(first)
	first[0] = "被呼叫端改掉時不該影響判定標準"

	second := Required()
	if len(second) != total {
		t.Fatalf("副本改動不該影響長度，實際 %d 筆", len(second))
	}
	if second[0] != "index.html" {
		t.Errorf("必須回傳副本，實際首筆為 %q", second[0])
	}
}

func TestPlaceholderNameIsHiddenFile(t *testing.T) {
	// go:embed 的 all: 前綴才會收進點開頭檔案；佔位檔的寫法改變時，內嵌樣式要一起檢視。
	if !strings.HasPrefix(PlaceholderName, ".") {
		t.Errorf("佔位檔應以「.」開頭，實際 %q", PlaceholderName)
	}
	if !strings.Contains(PlaceholderNote, "go:embed") {
		t.Error("佔位檔內容應說明它存在的理由，否則下一個看到它的人只會把它刪掉")
	}
}

// readErrorFS 讓 flutter_bootstrap.js 能用 Stat 查到、但讀取一律失敗。
type readErrorFS struct{ fstest.MapFS }

// ReadFile 取代 MapFS 的讀取捷徑，讓「查得到檔案但讀不了」得以重現。
func (f readErrorFS) ReadFile(name string) ([]byte, error) {
	if name == BootstrapFile {
		return nil, errors.New("模擬讀取失敗")
	}
	return f.MapFS.ReadFile(name)
}
