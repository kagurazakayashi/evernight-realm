package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

// withBodyLimit 限制單次請求可讀取的位元組數（server.max_body_bytes）。
// 超過上限時讀取回傳 *http.MaxBytesError，由 decodeJSON 轉為穩定的 1003 錯誤碼；
// 不讀取本體的路徑（如 /health）不受影響。
func (s *Server) withBodyLimit(next http.Handler) http.Handler {
	maxBytes := s.cfg.Server.MaxBodyBytes
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// withTimeout 為每次處理加上期限（server.request_timeout_ms）：
// 逾時且尚未送出回應時回 503 與穩定錯誤碼 1006，避免連線期限先觸發而讓用戶端只見連線中斷。
// 處理函式需尊重 r.Context() 才會即時中止；已開始送出的回應無法改寫狀態碼，僅寫入日誌。
func (s *Server) withTimeout(next http.Handler) http.Handler {
	timeout := time.Duration(s.cfg.Server.RequestTimeoutMS) * time.Millisecond
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		recorder := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r.WithContext(ctx))

		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return
		}
		s.logger.Printf("請求處理逾時：request_id=%s method=%s path=%s timeout=%s",
			requestIDFromRequest(r), r.Method, r.URL.Path, timeout)
		if recorder.wrote {
			return
		}
		writeError(recorder, r, CodeRequestTimeout, http.StatusServiceUnavailable)
	})
}

// isJSONContentType 檢查內容型別是否為 JSON：接受 application/json 與 +json 後綴型別。
func isJSONContentType(contentType string) bool {
	if strings.TrimSpace(contentType) == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

// unknownFieldPrefix 為 encoding/json 對未知欄位的固定訊息前綴（用於取出欄位名）。
const unknownFieldPrefix = "json: unknown field "

// decodeJSON 解析請求體 JSON 至 dst，並把輸入問題轉為穩定錯誤碼：
// 非 JSON 內容型別 → 1005、超過大小上限 → 1003、畸形／未知欄位／空本體／尾隨資料 → 1004。
// 解析成功回傳 true；失敗時已寫出錯誤信封並回傳 false，呼叫端應立即結束處理。
//
// 未知欄位一律視為錯誤（DisallowUnknownFields）：欄位拼錯時不得靜默忽略，
// 以免用戶端以為設定生效而實際未生效（SEC-008 輸入校驗）。
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeErrorDetails(w, r, CodeUnsupportedMediaType, http.StatusUnsupportedMediaType,
			map[string]any{"expected_content_type": "application/json"})
		return false
	}

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		code, status, details := classifyDecodeError(err)
		writeErrorDetails(w, r, code, status, details)
		return false
	}

	// 只接受單一 JSON 值：尾隨資料視為畸形請求，不靜默忽略。
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeErrorDetails(w, r, CodeInvalidBody, http.StatusBadRequest,
			map[string]any{"reason": "unexpected trailing data"})
		return false
	}
	return true
}

// classifyDecodeError 將 JSON 解碼錯誤映射為穩定錯誤碼與可公開的細節。
// 細節只描述用戶端輸入本身（欄位名、語法位置、上限值），不帶入伺服器內部型別或堆疊。
func classifyDecodeError(err error) (ErrorCode, int, map[string]any) {
	var maxBytesErr *http.MaxBytesError
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError

	switch {
	case errors.As(err, &maxBytesErr):
		return CodePayloadTooLarge, http.StatusRequestEntityTooLarge,
			map[string]any{"limit_bytes": maxBytesErr.Limit}
	case errors.Is(err, io.EOF):
		return CodeInvalidBody, http.StatusBadRequest,
			map[string]any{"reason": "empty request body"}
	case errors.Is(err, io.ErrUnexpectedEOF):
		return CodeInvalidBody, http.StatusBadRequest,
			map[string]any{"reason": "truncated request body"}
	case errors.As(err, &syntaxErr):
		return CodeInvalidBody, http.StatusBadRequest,
			map[string]any{"reason": "invalid JSON syntax", "offset": syntaxErr.Offset}
	case errors.As(err, &typeErr):
		return CodeInvalidBody, http.StatusBadRequest,
			map[string]any{"reason": "invalid value type", "field": typeErr.Field}
	case strings.HasPrefix(err.Error(), unknownFieldPrefix):
		return CodeInvalidBody, http.StatusBadRequest,
			map[string]any{"reason": "unknown field", "field": strings.Trim(err.Error()[len(unknownFieldPrefix):], `"`)}
	default:
		return CodeInvalidBody, http.StatusBadRequest,
			map[string]any{"reason": "invalid request body"}
	}
}
