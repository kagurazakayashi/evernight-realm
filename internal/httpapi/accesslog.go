package httpapi

import (
	"net"
	"net/http"
	"time"
)

// accessMessage 為訪問日誌的訊息文字（固定字串，判讀靠欄位而不是靠句子）。
const accessMessage = "請求完成"

// accessSlowWarningMS 超過此處理時間即改記為 WARN。
//
// 未觸發 server.request_timeout_ms 但明顯偏慢的請求，是容量問題最早的訊號；
// 全部記成 INFO 會讓這個訊號埋在探活的流水裡。
const accessSlowWarningMS = 1000

// withAccessLog 為每個請求寫一列結構化訪問日誌：方法、路徑、狀態碼、處理時間、
// 來源位址、協定版本、回應大小與關聯 ID。
//
// 本層刻意不讀請求本體：能記下的只有請求行與標頭，因此正文（PIN、會話令牌、
// 聊天內容）在結構上就進不了日誌，而不是靠「記得不要寫」這種約定。
// 狀態碼以 400 為界改用 WARN／ERROR，讓被拒絕與出故障的請求在層級過濾後仍看得見。
func (s *Server) withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)

		elapsedMS := time.Since(started).Milliseconds()
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration_ms", elapsedMS,
			"request_id", requestIDFromRequest(r),
			"remote_addr", remoteHost(r),
			"proto", r.Proto,
			"resp_bytes", recorder.bytes,
		}
		if query := r.URL.RawQuery; query != "" {
			attrs = append(attrs, "query", query)
		}
		if agent := r.Header.Get("User-Agent"); agent != "" {
			attrs = append(attrs, "user_agent", agent)
		}

		switch {
		case recorder.status >= http.StatusInternalServerError:
			s.logger.Error(accessMessage, attrs...)
		case recorder.status >= http.StatusBadRequest || elapsedMS >= accessSlowWarningMS:
			s.logger.Warn(accessMessage, attrs...)
		default:
			s.logger.Info(accessMessage, attrs...)
		}
	})
}

// statusRecorder 記錄最終狀態碼與回應位元組數。
//
// 狀態碼只記第一次 WriteHeader 的值：net/http 在標頭送出後的重複呼叫本來就不會
// 改變回應，日誌若跟著後一次寫的值，就會出現「回應 200、日誌 500」這種對不上的記錄。
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

// WriteHeader 記錄狀態碼後轉呼叫底層寫入器。
func (rec *statusRecorder) WriteHeader(status int) {
	if !rec.wroteHeader {
		rec.status = status
		rec.wroteHeader = true
	}
	rec.ResponseWriter.WriteHeader(status)
}

// Write 累計回應位元組數（未先呼叫 WriteHeader 時依 net/http 慣例視為 200）。
func (rec *statusRecorder) Write(b []byte) (int, error) {
	rec.wroteHeader = true
	n, err := rec.ResponseWriter.Write(b)
	rec.bytes += n
	return n, err
}

// Unwrap 讓 http.ResponseController 仍可取用底層寫入器的 Flush、Hijack 等能力。
func (rec *statusRecorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

// remoteHost 取連線來源位址（去掉埠號）。
//
// 解析不出來時原樣保留：寧可日誌裡出現一個看不懂的形狀，
// 也不要它變成空字串而讓人以為請求沒有來源。
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		return r.RemoteAddr
	}
	return host
}
