// Package app 提供服務端啟動入口與版本資訊（最小骨架）。
//
// 啟動函式與 main 分離：main 僅負責組裝與結束碼轉換，
// 實際啟動流程、錯誤處理與日後各模組的組合皆以本包為核心。
package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/kagurazakayashi/evernight-realm/internal/config"
	"github.com/kagurazakayashi/evernight-realm/internal/httpapi"
)

// Version 為目前開發版本。正式版號策略待發布流程定案後統一管理。
const Version = "0.1.0-dev"

// Run 啟動服務端，阻塞至收到停止信號（Ctrl+C、SIGTERM）或發生錯誤。
//
// ctx 為服務的根 context，訊號取消即代表停止請求；日後的背景任務
// （保留期清理、備份排程等）皆須以此 ctx 為取消來源並在返回前結束，
// 確保程序退出時不留殘留工作。正常停止回傳 nil，結束碼為 0。
func Run(args []string) error {
	ctx, releaseSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer releaseSignals()
	return run(ctx, releaseSignals, args, os.Stdout)
}

// run 為 Run 的可測試主體：ctx 取消即觸發優雅停止（正式路徑由訊號觸發）。
//
// releaseSignals 於停止流程開始時呼叫，用以還原預設訊號處理，
// 使停止期間再次按 Ctrl+C 可立即中止，不必等完優雅停止期限。
// 流程：解析命令列 → 載入並校驗組態 → 路徑規範化 → 資料目錄初始化
// → 監聽 → 輸出脫敏摘要與監聽提示 → 服務至停止請求 → 優雅停止。
// 組態非法或目錄不可用時回傳錯誤（含欄位路徑，不含機密），由 main 決定結束碼。
func run(ctx context.Context, releaseSignals func(), args []string, out io.Writer) error {
	opts, err := config.ParseArgs(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(opts)
	if err != nil {
		return err
	}
	if err := cfg.Resolve(); err != nil {
		return err
	}
	if err := cfg.Prepare(); err != nil {
		return err
	}

	// 先建立監聽器再輸出啟動資訊：連接埠被佔用等失敗直接回報，
	// 且輸出的是實際監聽地址（監聽埠設為 0 時可見系統指派的埠號）。
	srv := httpapi.New(&cfg, Version)
	ln, err := srv.Listen()
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "evernight-server %s\n", Version)
	fmt.Fprintln(out, cfg.Redacted())
	if cfg.ListenAllInterfaces() {
		fmt.Fprintln(out, "風險提示：監聽地址暴露於所有介面（含公網網卡），請確認防火牆與部署範圍。")
	}
	fmt.Fprintf(out, "HTTP 服務已啟動：http://%s （/health 為存活檢查）\n", ln.Addr())

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		// 服務在收到停止請求前自行結束（如監聽器失效）：無優雅停止流程可跑。
		return err
	case <-ctx.Done():
	}

	// 停止流程：停止接受新連線、等待進行中的請求完成（上限為 shutdown_timeout_ms）；
	// 逾時則強制關閉連線，確保程序結束並釋放監聽資源。
	releaseSignals()
	timeout := srv.ShutdownTimeout()
	fmt.Fprintf(out, "收到停止信號，開始優雅停止（最長等待 %s）\n", timeout)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(out, "優雅停止逾時（%v），強制關閉連線\n", err)
		if closeErr := srv.Close(); closeErr != nil {
			return fmt.Errorf("app: 強制關閉服務失敗: %w", closeErr)
		}
	}
	if err := <-serveErr; err != nil {
		return err
	}
	fmt.Fprintln(out, "服務已停止，監聽資源已釋放。")
	return nil
}
