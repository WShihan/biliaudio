package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/WShihan/biliaudio/internal/buildinfo"
	"github.com/WShihan/biliaudio/internal/web"
)

// isServeCommand 判断是否要启动 Web 服务。
func isServeCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "serve", "web", "server":
		return true
	default:
		return false
	}
}

// runServe 启动内嵌资源版的 HTTP 服务。
func runServe(args []string) int {
	console := NewConsole()

	cfg := struct {
		addr    string
		cookie  string
		token   string
		timeout time.Duration
	}{}

	flags := flag.NewFlagSet("biliaudio serve", flag.ContinueOnError)
	flags.SetOutput(console.Err)
	flags.StringVar(&cfg.addr, "addr", ":8080", "监听地址，如 :8080 或 127.0.0.1:8080")
	flags.StringVar(&cfg.cookie, "cookie", os.Getenv("BILI_COOKIE"), "B 站登录态（默认读环境变量 BILI_COOKIE）")
	flags.StringVar(&cfg.token, "token", os.Getenv("BILIAUDIO_TOKEN"), "访问令牌；留空则不校验（默认读 BILIAUDIO_TOKEN）")
	flags.DurationVar(&cfg.timeout, "timeout", 10*time.Minute, "单次请求超时")
	flags.Usage = func() {
		fmt.Fprint(console.Err, `biliaudio serve - 启动网页版（静态资源已内嵌进二进制）

用法:
  biliaudio serve [选项]

选项:
      -addr <地址>     监听地址（默认 :8080）
      -token <令牌>    访问令牌，留空则不校验
      -cookie <Cookie> B 站登录态，默认读 BILI_COOKIE
      -timeout <时长>   单次请求超时（默认 10m）

示例:
  biliaudio serve -addr :8080
  biliaudio serve -addr 127.0.0.1:8080          # 只允许本机，前面挂 Nginx
  biliaudio serve -addr :8080 -token mysecret   # 手机上访问 http://host:8080/?token=mysecret

环境变量:
  BILI_COOKIE        B 站 Cookie（可选，解锁更高音质）
  BILIAUDIO_TOKEN    访问令牌
  BILIAUDIO_DEBUG    非空时输出调试日志（含转发中断）
`)
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		console.Error("%v", err)
		return exitUsage
	}

	if cfg.token == "" {
		cfg.token = generateToken()
		console.Warn("未设置 -token，已临时生成一个：%s", cfg.token)
		console.Warn("该令牌**每次启动都会变**，旧地址会失效；建议固定：-token %s", cfg.token)
	}

	handler, err := web.NewHandler(web.Config{
		Addr:    cfg.addr,
		Cookie:  cfg.cookie,
		Token:   cfg.token,
		Timeout: cfg.timeout,
		Logger:  web.NewLogger(console.Err),
	})
	if err != nil {
		console.Error("初始化失败：%v", err)
		return exitError
	}

	server := &http.Server{
		Addr:              cfg.addr,
		Handler:           logRequests(handler, console),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		console.Error("无法监听 %s：%v", cfg.addr, err)
		return exitError
	}

	printServeBanner(console, listener, cfg.token, cfg.cookie != "")
	console.Info("按 Ctrl-C 停止服务")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		console.Error("服务异常退出：%v", err)
		return exitError
	case <-ctx.Done():
		console.Info("")
		console.Info("正在关闭…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			console.Warn("关闭时出错：%v", err)
		}
	}
	return exitOK
}

// printServeBanner 打印访问地址，尽量把局域网 IP 也列出来方便手机访问。
func printServeBanner(console *Console, listener net.Listener, token string, hasCookie bool) {
	port := "8080"
	if addr, ok := listener.Addr().(*net.TCPAddr); ok {
		port = fmt.Sprint(addr.Port)
	}

	console.Info("")
	console.Success("biliaudio 网页版已启动")
	console.Field("版本", buildinfo.Summary())
	console.Field("Go", buildinfo.Runtime())
	console.Field("本机", fmt.Sprintf("http://127.0.0.1:%s/?token=%s", port, token))
	for _, ip := range localIPs() {
		console.Field("局域网", fmt.Sprintf("http://%s:%s/?token=%s", ip, port, token))
	}
	console.Field("Cookie", enabledText(hasCookie)+"（BILI_COOKIE，可解锁更高音质）")
	console.Field("模板指纹", web.TemplateFingerprint())
	console.Field("资源版本", "style.css / player.js 的 ?v="+web.AssetVersion())
	if err := web.TextareaIsClean(); err != nil {
		console.Error("模板自检未通过：%v", err)
	} else {
		console.Field("页面自检", "通过（输入框初始值为空）")
	}
	console.Info("")
	console.Dim("  提示：手机与电脑连同一个 Wi-Fi 即可用上面的局域网地址；")
	console.Dim("  部署到 VPS 时建议只监听 127.0.0.1 并用 Nginx 转发 + HTTPS。")
	console.Info("")
}

func enabledText(ok bool) string {
	if ok {
		return "已配置"
	}
	return "未配置"
}

// localIPs 列出本机的局域网 IPv4 地址。
func localIPs() []string {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var ips []string
	for _, address := range addresses {
		network, ok := address.(*net.IPNet)
		if !ok || network.IP.IsLoopback() {
			continue
		}
		ip4 := network.IP.To4()
		if ip4 == nil {
			continue
		}
		ips = append(ips, ip4.String())
	}
	return ips
}

// generateToken 生成随机访问令牌。
func generateToken() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "biliaudio"
	}
	return hex.EncodeToString(buffer)
}

// logRequests 记录访问日志，方便在 VPS 上排查。
func logRequests(next http.Handler, console *Console) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 静态资源与健康检查太吵，不记录
		switch {
		case len(r.URL.Path) >= 8 && r.URL.Path[:8] == "/static/",
			r.URL.Path == "/favicon.ico",
			r.URL.Path == "/healthz":
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		console.Dim("%s %s %d %s", r.Method, r.URL.Path, recorder.status,
			time.Since(started).Round(time.Millisecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Flush 让流式响应（音频转发）能正常工作。
func (s *statusRecorder) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
