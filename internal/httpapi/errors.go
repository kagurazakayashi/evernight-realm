package httpapi

import (
	"encoding/json"
	"net/http"
)

// ErrorCode 為對外的穩定機器錯誤碼。
//
// 分段規劃（S02 決策）：1xxx 通用與協定層、2xxx 帳號與身分、3xxx 資產與帳務，
// 其餘分段隨端點實作細分。已發布的數值不得變更或重用。
type ErrorCode int

const (
	// CodeUnknown 表示未分類的內部錯誤；對外只回固定文案，不洩漏細節。
	CodeUnknown ErrorCode = 1000
	// CodeNotFound 表示請求的路徑不存在。
	CodeNotFound ErrorCode = 1001
	// CodeMethodNotAllowed 表示路徑存在，但不支援該 HTTP 方法。
	CodeMethodNotAllowed ErrorCode = 1002
)

// ErrorEnvelope 是所有錯誤回應的統一信封，也是錯誤回應格式的唯一權威定義。
//
// 欄位依 S02 決策固定為穩定數字錯誤碼、在地化使用者訊息、選用細節與請求關聯 ID；
// Details 目前尚無端點填入，保留為契約欄位，值為空時不出現在回應中。
type ErrorEnvelope struct {
	Code      ErrorCode      `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
	RequestID string         `json:"request_id"`
}

// writeJSON 以 JSON 內容型別輸出回應；標頭送出後才編碼，編碼失敗無法再改寫狀態碼。
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeError 以統一信封輸出錯誤回應：狀態碼依 status，訊息依 Accept-Language 協商。
func writeError(w http.ResponseWriter, r *http.Request, code ErrorCode, status int) {
	writeJSON(w, status, ErrorEnvelope{
		Code:      code,
		Message:   messageFor(code, negotiateLocale(r.Header.Get("Accept-Language"))),
		RequestID: requestIDFromRequest(r),
	})
}
