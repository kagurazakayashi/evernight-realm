package runlog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// dateLayout 為日誌檔名與保留比對用的日期寫法（ISO 8601 的日粒度）。
//
// 選它而不是緊湊數字：同一目錄下用肉眼就看得出那份檔屬於哪一天；
// 而零填補的 ISO 日期，字串比對（date < cutoff）恰好等於日期先後，淘汰時不必再解析一次。
const dateLayout = "2006-01-02"

// maintenancePrefix 標示「這一行是日誌子系統自己的維護訊息」，不是業務記錄。
const maintenancePrefix = "日誌維護："

// DefaultFilePrefix 為日誌檔名前綴的內建預設（與 config.Default() 一致）。
const DefaultFilePrefix = "evernight-run"

// ErrSinkClosed 表示日誌檔案已被 Close，之後不再接受寫入。
var ErrSinkClosed = errors.New("runlog: 日誌寫入器已關閉")

// LogFileName 回傳指定時刻所屬自然日使用的檔案名（<前綴>.<YYYY-MM-DD>.log）。
//
// 日粒度由 loc 決定：本協議的時間戳一律 UTC（DEC-015），但「這天的日誌」是部署者的生活經驗，
// 因此分檔跟的是顯示時區，记录内容仍是 UTC——兩者不對齊是刻意且已寫進文件的事實。
func LogFileName(prefix string, at time.Time, loc *time.Location) string {
	if prefix == "" {
		prefix = DefaultFilePrefix
	}
	if loc == nil {
		loc = time.UTC
	}
	return fmt.Sprintf("%s.%s.log", prefix, at.In(loc).Format(dateLayout))
}

// daySink 為按自然日切換的日誌檔案寫入器，同時負責按天數淘汰舊檔。
//
// 換日不靠排程也不靠重新啟動：每次寫入前比一次日期，跨日的第一條記錄把檔切過去。
// 區域網單機服務本來就沒有什麼流量，靠時間驅動的排程只會多出一個「排程沒跑成」的失敗路徑。
type daySink struct {
	dir           string
	prefix        string
	loc           *time.Location
	now           func() time.Time
	retentionDays int
	// note 為換日與淘汰動作的回報去處（一般為標準錯誤輸出）。
	// 這些動作本身發生在寫入途中，不能再經記錄器回報，否則就是自己寫自己。
	note io.Writer

	mu   sync.Mutex
	file *os.File
	date string
	path string
}

// newDaySink 建立寫入器並立即定位到「今天」那份檔案（必要時建立目錄與檔案）。
func newDaySink(dir, prefix string, loc *time.Location, now func() time.Time, retentionDays int, note io.Writer) (*daySink, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("runlog: 日誌目錄不可為空")
	}
	if prefix == "" {
		prefix = DefaultFilePrefix
	}
	if retentionDays < 0 {
		return nil, fmt.Errorf("runlog: 保留天數不可為負數，實際為 %d", retentionDays)
	}
	if loc == nil {
		// time.Time.In(nil) 會 panic：零值 Options（未給時區的直接呼叫者，含測試）
		// 也要有確定行為，因此在這裡定型為 UTC。
		loc = time.UTC
	}
	if now == nil {
		now = time.Now
	}
	s := &daySink{dir: dir, prefix: prefix, loc: loc, now: now, retentionDays: retentionDays, note: note}
	if err := s.openToday(); err != nil {
		return nil, err
	}
	return s, nil
}

// Path 回傳目前正要寫入的檔案路徑（換日後跟著改變）。
func (s *daySink) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

// Date 回傳目前檔案所屬的自然日（顯示時區）。
func (s *daySink) Date() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.date
}

// Write 在換日時先切檔，然後把這串位元組交給當日檔案。
func (s *daySink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if today := s.dayOf(s.now()); today != s.date {
		if err := s.rotateLocked(today); err != nil {
			// 開新檔失敗時不把記錄無聲吞掉：繼續寫在還開著的舊檔上，
			// 並向回報去處講一句。檔案名的日期會落後實際內容，
			// 但「這天發生了什麼」比「檔名好不好看」重要。
			s.reportf("日誌換日失敗（沿用 %s）：%v", s.path, err)
			if s.file == nil {
				return 0, err
			}
		}
	}
	if s.file == nil {
		return 0, ErrSinkClosed
	}
	return s.file.Write(p)
}

// Close 關閉當日檔案；重複關閉回傳第一次的結果。
func (s *daySink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// openToday 建立/開啟「今天」那份檔案，並依保留天數淘汰過期檔。
func (s *daySink) openToday() error {
	return s.rotateLocked(s.dayOf(s.now()))
}

// rotateLocked 切到指定自然日的檔案。呼叫端必須已持有 mu。
func (s *daySink) rotateLocked(date string) error {
	path := filepath.Join(s.dir, fmt.Sprintf("%s.%s.log", s.prefix, date))
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("開啟日誌檔案 %s 失敗: %w", path, err)
	}
	if s.file != nil && s.file != file {
		if closeErr := s.file.Close(); closeErr != nil {
			// 舊檔關不掉不影響新檔可用（Windows 上浮留的句柄只會讓後續手動搬檔失敗），
			// 因此記在回報裡但不以此報失敗——否則一次清理瑕疵會讓日誌整個停寫。
			s.reportf("關閉前一份日誌失敗：%v", closeErr)
		}
	}
	s.file = file
	s.date = date
	s.path = path

	if deleted := s.cleanupLocked(date); deleted > 0 {
		s.reportf("日誌保留清理：已刪除 %d 份過期檔案（保留 %d 天）", deleted, s.retentionDays)
	}
	return nil
}

// cleanupLocked 刪除超出保留天數的日誌檔，回傳刪除數。
//
// 只認得「自己檔名的形狀」（<前綴>.<YYYY-MM-DD>.log）：不符合的一律不動。
// 這不是省事，而是必須——普通日誌可輪替、可淘汰，審計記錄與別的檔案不行（規格 §32）。
// 同一個 logs 目錄裡放著永久審計帳本時，本函式照樣一個位元組都不會碰它。
func (s *daySink) cleanupLocked(today string) int {
	if s.retentionDays <= 0 {
		// 0 代表不限制：預設值。要「長期運行不會無限增大」由部署者明確設一個正值。
		return 0
	}
	cutoff, ok := shiftDate(today, -(s.retentionDays - 1))
	if !ok {
		s.reportf("日誌保留清理：今日日期無法解析（%q），本次不刪除任何檔案", today)
		return 0
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		s.reportf("日誌保留清理：讀取目錄 %s 失敗：%v", s.dir, err)
		return 0
	}
	deleted := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		date, own := s.dateOfName(entry.Name())
		if !own || date >= cutoff {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, entry.Name())); err != nil {
			s.reportf("日誌保留清理：刪除 %s 失敗：%v", entry.Name(), err)
			continue
		}
		deleted++
	}
	return deleted
}

// dateOfName 解析檔案名是否屬於本寫入器的日誌檔，並取出其日期。
func (s *daySink) dateOfName(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, s.prefix+".")
	if !ok {
		return "", false
	}
	date, ok := strings.CutSuffix(rest, ".log")
	if !ok || len(date) != len(dateLayout) {
		return "", false
	}
	if _, err := time.Parse(dateLayout, date); err != nil {
		return "", false
	}
	return date, true
}

// dayOf 回傳時刻在顯示時區裡的自然日。
func (s *daySink) dayOf(at time.Time) string {
	return at.In(s.loc).Format(dateLayout)
}

// reportf 把維護動作（換日、保留淘汰）的結果寫到回報去處。
//
// 不經記錄器：這些動作發生在寫入途中，再走一次記錄器就是自己寫自己。
// 行首帶「日誌維護：」把它與記錄行分開——兩邊的時間戳形狀一模一樣，
// 混在一起之後「日誌行數等於記錄數」這種判據就失效了。
func (s *daySink) reportf(format string, args ...any) {
	if s.note == nil {
		return
	}
	h := newHumanHandler(s.note, slog.LevelWarn, nil)
	_ = h.Handle(context.Background(), slog.NewRecord(time.Now().UTC(), slog.LevelWarn, maintenancePrefix+fmt.Sprintf(format, args...), 0))
}

// shiftDate 把 ISO 日期加減若干天；解析失敗回 false（寧可不刪，也不要刪錯）。
func shiftDate(date string, days int) (string, bool) {
	parsed, err := time.Parse(dateLayout, date)
	if err != nil {
		return "", false
	}
	return parsed.AddDate(0, 0, days).Format(dateLayout), true
}
