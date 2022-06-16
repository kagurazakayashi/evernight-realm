package httpapi

import (
	"context"
	"net/http"
	"runtime/debug"
)

// requestIDHeader 為請求關聯 ID 的標頭名稱：用戶端可帶入以自行關聯，回應一律回傳。
const requestIDHeader = "X-Request-ID"

// requestIDMaxLen 限制可透傳的用戶端關聯 ID 長度。
const requestIDMaxLen = 64

// requestIDKey 為關聯 ID 在請求 context 中的鍵型別（未匯出，避免外部以其他鍵覆寫）。
type requestIDKey string

// requestIDCtxKey 為關聯 ID 在請求 context 中的實際鍵值。
const requestIDCtxKey requestIDKey = "request_id"

// middleware 為中介層型別：包裝處理器並回傳新的處理器。
type middleware func(http.Handler) http.Handler

// chain 依序套用中介層，第一個為最外層（最先收到請求、最後結束）。
func chain(h http.Handler, middlewares ...middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

// validRequestID 檢查用戶端帶入的關聯 ID 是否可安全透傳。
// 只接受長度受限的 [A-Za-z0-9._-]，避免控制字元造成標頭或日誌注入。
//
// 這裡刻意不以 idgen.Parse 判定：關聯 ID 由用戶端自行選定、只用於比對其自身日誌，
// 不是內部實體主鍵；只有「改由伺服器產生」時才一律經 idgen。
func validRequestID(id string) bool {
	if id == "" || len(id) > requestIDMaxLen {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// withRequestID 為每個請求決定關聯 ID：沿用用戶端合法值，否則經 idgen 產生 UUIDv7。
// ID 同時放入回應標頭與請求 context，供錯誤信封與伺服器端日誌使用。
//
// 產生失敗時不降級為其他版本或隨機字串（協議要求伺服器產生的 ID 恆為 UUIDv7），
// 改以統一錯誤信封拒絕本次請求，原因只寫伺服器端日誌。
func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !validRequestID(id) {
			gen, err := s.newID()
			if err != nil {
				s.logger.Printf("產生請求關聯 ID 失敗：method=%s path=%s err=%v", r.Method, r.URL.Path, err)
				writeError(w, r, CodeUnknown, http.StatusInternalServerError)
				return
			}
			id = gen.String()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDCtxKey, id)))
	})
}

// requestIDFromContext 取得請求關聯 ID；未經中介層時回傳空字串。
func requestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDCtxKey).(string)
	return id
}

// requestIDFromRequest 取得當前請求的關聯 ID。
func requestIDFromRequest(r *http.Request) string {
	return requestIDFromContext(r.Context())
}

// responseRecorder 記錄回應標頭是否已送出，供 panic 恢復判斷能否改寫狀態碼。
// Unwrap 讓 http.ResponseController 仍可取用底層寫入器的 Flush、Hijack 等能力。
type responseRecorder struct {
	http.ResponseWriter
	wrote bool
}

// WriteHeader 記錄標頭已送出後轉呼叫底層寫入器。
func (rec *responseRecorder) WriteHeader(status int) {
	rec.wrote = true
	rec.ResponseWriter.WriteHeader(status)
}

// Write 記錄回應已開始後轉呼叫底層寫入器。
func (rec *responseRecorder) Write(b []byte) (int, error) {
	rec.wrote = true
	return rec.ResponseWriter.Write(b)
}

// Unwrap 回傳底層寫入器。
func (rec *responseRecorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

// withRecovery 攔截處理函式中的 panic：完整資訊（含關聯 ID 與堆疊）只寫入伺服器端日誌，
// 對外回傳統一的內部錯誤信封，不洩漏堆疊或 panic 內容。
// http.ErrAbortHandler 屬連線中止的既有語意，交還 net/http 處理而不記為異常。
func (s *Server) withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &responseRecorder{ResponseWriter: w}
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if recovered == http.ErrAbortHandler {
				panic(recovered)
			}
			s.logger.Printf("已攔截 panic：request_id=%s method=%s path=%s panic=%v\n%s",
				requestIDFromRequest(r), r.Method, r.URL.Path, recovered, debug.Stack())
			if recorder.wrote {
				// 回應已開始送出，狀態碼無法改寫，僅中止本次處理。
				return
			}
			writeError(recorder, r, CodeUnknown, http.StatusInternalServerError)
		}()
		next.ServeHTTP(recorder, r)
	})
}
