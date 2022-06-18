// Package httpapi 提供服務端 HTTP 層：路由掛載、中介層鏈、統一錯誤信封與存活檢查端點。
//
// 業務端點一律掛在根路徑（無版本前綴，依 S02 決策），由 registerRoutes 集中登記。
// 基礎端點有三個：/health 回答進程存活、/ready 回答依賴（資料庫）就緒與否、
// /time 回答伺服器當前時間與顯示時區；三者都是 GET/HEAD，且不採信請求內容提供的時間。
// 輸入保護（請求體上限、處理期限、連線層期限、JSON 解碼限制）於中介層與 http.Server 設定；
// 安全回應頭（CSP、內容型別保護、Frame 限制等）由 withSecurityHeaders 對所有回應套用。
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kagurazakayashi/evernight-realm/internal/config"
	"github.com/kagurazakayashi/evernight-realm/internal/idgen"
	"github.com/kagurazakayashi/evernight-realm/internal/timeutil"
)

// Deps 為 HTTP 服務層的外部依賴；零值表示沒有外部依賴。
type Deps struct {
	// Ready 為就緒檢查：回傳錯誤表示業務尚不可用（例如資料庫無法回應）。
	// 為 nil 時表示本服務沒有外部依賴，程序存活即視為就緒。
	Ready func(context.Context) error
	// Clock 為業務時間來源；為 nil 時採用 timeutil.System()。
	Clock timeutil.Clock
}

// Server 為 HTTP 服務層。
type Server struct {
	cfg     *config.Config
	version string
	logger  *log.Logger
	httpSrv *http.Server
	// secHeaders 為啟動時算好的安全回應頭（組態留空時為內建基線）。
	secHeaders securityHeaderSet
	// newID 為伺服器側標識的產生器，固定為 idgen.New（全服務唯一產生點）；
	// 以欄位持有是為了讓測試能注入失敗情境，驗證該路徑不降級而是拒絕請求。
	newID func() (idgen.ID, error)
	// ready 為就緒檢查（可為 nil）；與 newID 同樣以欄位持有，供測試注入失敗情境。
	ready func(context.Context) error
	// clock 為業務時間來源；時刻一律取自此處，不接受請求內容提供的時間。
	clock timeutil.Clock
	// displayZone 為啟動時由組態解析出的顯示時區，供時間回應輸出 UTC 偏移。
	displayZone *time.Location
}

// New 以組態、版本字串與外部依賴建立 HTTP 服務層；伺服器端日誌固定寫往標準錯誤輸出。
func New(cfg *config.Config, version string, deps Deps) *Server {
	clock := deps.Clock
	if clock == nil {
		clock = timeutil.System()
	}
	s := &Server{
		cfg:         cfg,
		version:     version,
		logger:      log.New(os.Stderr, "evernight-server ", log.LstdFlags),
		secHeaders:  buildSecurityHeaders(cfg),
		newID:       idgen.New,
		ready:       deps.Ready,
		clock:       clock,
		displayZone: cfg.DisplayLocation(),
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
// 中介層由外而內為：安全回應頭 → 請求關聯 ID → panic 恢復 → 處理期限 → 請求體上限 → 路由。
//
// 安全回應頭固定最外層，讓鏈上任何一層自行寫出的回應（含無法產生關聯 ID 時的拒絕）
// 都帶著標頭，不外洩未受保護的回應。
func (s *Server) wrap(h http.Handler) http.Handler {
	return chain(h, s.withSecurityHeaders, s.withRequestID, s.withRecovery, s.withTimeout, s.withBodyLimit)
}

// registerRoutes 集中登記路由；未登記的路徑與不支援的方法都回傳統一錯誤信封。
func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", s.allowMethods(s.handleHealth, http.MethodGet, http.MethodHead))
	mux.HandleFunc("/ready", s.allowMethods(s.handleReady, http.MethodGet, http.MethodHead))
	mux.HandleFunc("/time", s.allowMethods(s.handleTime, http.MethodGet, http.MethodHead))
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

// Listen 依組態建立 TCP 監聽器；失敗時回傳含地址的錯誤（如連接埠被佔用）。
//
// 監聽與服務分離，讓呼叫端（internal/app）能先取得實際地址再啟動服務，
// 並在停止時掌握監聽資源的生命週期。
func (s *Server) Listen() (net.Listener, error) {
	ln, err := net.Listen("tcp", s.cfg.Server.Listen)
	if err != nil {
		return nil, fmt.Errorf("httpapi: 監聽 %s 失敗: %w", s.cfg.Server.Listen, err)
	}
	return ln, nil
}

// Serve 在已建立的監聽器上提供服務，阻塞至服務停止或異常終止。
// 正常停止（呼叫 Shutdown 或 Close）回傳 nil，其他錯誤包裝後回傳。
func (s *Server) Serve(ln net.Listener) error {
	if err := s.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("httpapi: 服務異常終止: %w", err)
	}
	return nil
}

// Shutdown 優雅停止服務：停止接受新連線、等待進行中的請求完成，並釋放監聽資源。
// ctx 逾時時回傳 ctx.Err()，由呼叫端決定是否改以 Close 強制關閉。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}

// Close 立即關閉服務與所有連線，不等待進行中的請求；
// 僅用於優雅停止逾時的兵底，正常停止請用 Shutdown。
func (s *Server) Close() error {
	return s.httpSrv.Close()
}

// ShutdownTimeout 回傳組態的優雅停止等待上限，供呼叫端設定停止期限。
func (s *Server) ShutdownTimeout() time.Duration {
	return time.Duration(s.cfg.Server.ShutdownTimeoutMS) * time.Millisecond
}

// serviceName 為回應與日誌使用的服務識別名。
const serviceName = "evernight-server"

// readyCheckTimeout 為就緒檢查的等待上限。
//
// 資料庫一時的鎖競爭或延遲不應讓探測請求掛住；此值也必須短於
// server.request_timeout_ms（預設 10 秒），否則用戶端只會看到處理逾時（1006）
// 而不是明確的未就緒回應（1007）。
const readyCheckTimeout = 3 * time.Second

// healthResponse 為存活檢查回應；request_id 供用戶端對應伺服器端診斷日誌。
//
// 本端點只回答「進程還活著」，不代表業務可用——依賴狀態由 /ready 回答。
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
		Service:   serviceName,
		Version:   s.version,
		RequestID: requestIDFromRequest(r),
	})
}

// readyResponse 為就緒檢查的成功回應。
type readyResponse struct {
	Status    string `json:"status"`
	Service   string `json:"service"`
	RequestID string `json:"request_id"`
}

// handleReady 提供就緒檢查：外部依賴無法回應時回 503 與穩定錯誤碼，
// 不對外報告業務可用（規格 §27.2 的時間與資料來源須確實可用）。
//
// 判定失敗的內部原因（驅動訊息、資料庫路徑、連線池狀態）只寫伺服器端日誌，
// 回應一律是脫敏信封；未註冊依賴時視同已就緒（存活即業務可用）。
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.ready != nil {
		ctx, cancel := context.WithTimeout(r.Context(), readyCheckTimeout)
		defer cancel()
		if err := s.ready(ctx); err != nil {
			s.logger.Printf("就緒檢查失敗：request_id=%s err=%v", requestIDFromRequest(r), err)
			writeError(w, r, CodeNotReady, http.StatusServiceUnavailable)
			return
		}
	}
	writeJSON(w, http.StatusOK, readyResponse{
		Status:    "ready",
		Service:   serviceName,
		RequestID: requestIDFromRequest(r),
	})
}

// timeResponse 為伺服器時間查詢回應。
//
// time 一律為 UTC 的 RFC 3339 字串（恆含三位毫秒、以 Z 結尾，格式由 timeutil 統一負責）；
// timezone 為組態的 IANA 時區名稱，utc_offset_seconds 為該時刻在此時區的偏移秒數，
// 用戶端據此顯示當地時間而無需自帶時區資料庫（規格 §27.2）。
// 本端點不受就緒門控：時刻取自進程時鐘，資料庫短暫不可用時用戶端仍需校時與顯示斷線狀態。
type timeResponse struct {
	Time             string `json:"time"`
	Timezone         string `json:"timezone"`
	UTCOffsetSeconds int    `json:"utc_offset_seconds"`
	RequestID        string `json:"request_id"`
}

// handleTime 回應伺服器當前時間與顯示時區。
//
// 時刻一律由注入的時鐘產生，不接受也不採信請求內容提供的任何時間
// （規格 §27.2、SYS-006、DEC-015）。
func (s *Server) handleTime(w http.ResponseWriter, r *http.Request) {
	at := s.clock.Now()
	_, offset := at.In(s.displayZone).Zone()
	writeJSON(w, http.StatusOK, timeResponse{
		Time:             timeutil.FormatUTC(at),
		Timezone:         s.cfg.Server.DisplayTimezone,
		UTCOffsetSeconds: offset,
		RequestID:        requestIDFromRequest(r),
	})
}

// handleNotFound 為未登記路徑的統一 404 回應。
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, CodeNotFound, http.StatusNotFound)
}
