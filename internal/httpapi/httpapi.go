// Package httpapi 提供服務端 HTTP 層：路由掛載、存活檢查端點與未來 API 的掛載點。
//
// 目前僅提供 /health 存活檢查；統一錯誤信封、Request ID、請求限制與安全回應頭
// 於後續步驟加入。
package httpapi

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	"github.com/kagurazakayashi/evernight-realm/internal/config"
)

// Server 為 HTTP 服務層。
type Server struct {
	cfg     *config.Config
	version string
	httpSrv *http.Server
}

// New 以組態與版本字串建立 HTTP 服務層。
func New(cfg *config.Config, version string) *Server {
	s := &Server{cfg: cfg, version: version}
	s.httpSrv = &http.Server{
		Addr:    cfg.Server.Listen,
		Handler: s.Handler(),
	}
	return s
}

// Handler 回傳路由樹，供 http.Server 或測試伺服器使用。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	return mux
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

// handleHealth 提供存活檢查：GET /health → 200 與結構化狀態；其他方法 → 405。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"service": "evernight-server",
		"version": s.version,
	})
}
