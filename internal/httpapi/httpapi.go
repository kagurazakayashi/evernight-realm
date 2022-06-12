// Package httpapi 提供服務端 HTTP 層：路由掛載、中介層鏈、統一錯誤信封與存活檢查端點。
//
// 業務端點一律掛在根路徑（無版本前綴，依 S02 決策），由 registerRoutes 集中登記。
// 輸入保護（請求體上限、處理期限、連線層期限、JSON 解碼限制）於中介層與 http.Server 設定；
// 安全回應頭於後續步驟加入。
package httpapi

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kagurazakayashi/evernight-realm/internal/config"
)

// Server 為 HTTP 服務層。
type Server struct {
	cfg     *config.Config
	version string
	logger  *log.Logger
	httpSrv *http.Server
}

// New 以組態與版本字串建立 HTTP 服務層；伺服器端日誌固定寫往標準錯誤輸出。
func New(cfg *config.Config, version string) *Server {
	s := &Server{
		cfg:     cfg,
		version: version,
		logger:  log.New(os.Stderr, "evernight-server ", log.LstdFlags),
	}
	s.httpSrv = &http.Server{
		Addr:    cfg.Server.Listen,
		Handler: s.Handler(),
		// 連線層期限（STEP-034）：標頭、整個請求讀取、回應寫入與 keep-alive 空閒。
		ReadHeaderTimeout: time.Duration(cfg.Server.ReadHeaderTimeoutMS) * time.Millisecond,
		ReadTimeout:       time.Duration(cfg.Server.ReadTimeoutMS) * time.Millisecond,
		WriteTimeout:      time.Duration(cfg.Server.WriteTimeoutMS) * time.Millisecond,
		IdleTimeout:       time.Duration(cfg.Server.IdleTimeoutMS) * time.Millisecond,
		// 標頭總量上限：一般請求（含 Cookie）遠低於此值，用於擋標頭洪水。
		MaxHeaderBytes: maxHeaderBytes,
		ErrorLog:       s.logger,
	}
	return s
}

// maxHeaderBytes 為請求行與標頭總量上限（64 KiB）。
const maxHeaderBytes = 1 << 16

// Handler 回傳套用中介層鏈後的路由樹，供 http.Server 或測試伺服器使用。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	return s.wrap(mux)
}

// wrap 為路由樹套上完整中介層鏈（測試亦以本方法組裝，確保與正式路徑一致）。
// 中介層由外而內為：請求關聯 ID → panic 恢復 → 處理期限 → 請求體上限 → 路由。
func (s *Server) wrap(h http.Handler) http.Handler {
	return chain(h, withRequestID, s.withRecovery, s.withTimeout, s.withBodyLimit)
}

// registerRoutes 集中登記路由；未登記的路徑與不支援的方法都回傳統一錯誤信封。
func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", s.allowMethods(s.handleHealth, http.MethodGet, http.MethodHead))
	mux.HandleFunc("/", s.handleNotFound)
}

// allowMethods 包裝處理函式，只放行指定方法；其他方法回 405 並附 Allow 標頭。
func (s *Server) allowMethods(handler http.HandlerFunc, methods ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for _, method := range methods {
			if r.Method == method {
				handler(w, r)
				return
			}
		}
		w.Header().Set("Allow", strings.Join(methods, ", "))
		writeError(w, r, CodeMethodNotAllowed, http.StatusMethodNotAllowed)
	}
}

// ListenAndServe 監聽組態指定的地址並提供服務，阻塞至錯誤發生（如連接埠被佔用）。
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("httpapi: 監聽 %s 失敗: %w", s.cfg.Server.Listen, err)
	}
	if err := s.httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("httpapi: 服務異常終止: %w", err)
	}
	return nil
}

// healthResponse 為存活檢查回應；request_id 供用戶端對應伺服器端診斷日誌。
type healthResponse struct {
	Status    string `json:"status"`
	Service   string `json:"service"`
	Version   string `json:"version"`
	RequestID string `json:"request_id"`
}

// handleHealth 提供存活檢查：GET /health → 200 與結構化狀態。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Status:    "ok",
		Service:   "evernight-server",
		Version:   s.version,
		RequestID: requestIDFromRequest(r),
	})
}

// handleNotFound 為未登記路徑的統一 404 回應。
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, CodeNotFound, http.StatusNotFound)
}
