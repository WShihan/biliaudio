package web

import (
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

// sharedTransport 复用连接：代理音频时要连很多次同一个 CDN，
// 默认的 http.DefaultTransport 已经够用，这里只调整超时与连接上限。
var sharedTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          64,
	MaxIdleConnsPerHost:   16,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 20 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

// Logger 是极简日志器，默认输出到 stderr，与 systemd/docker 日志收集天然兼容。
type Logger struct {
	logger *log.Logger
	debug  bool
}

// NewLogger 创建日志器；w 为 nil 时输出到 stderr。
func NewLogger(w io.Writer) *Logger {
	if w == nil {
		w = os.Stderr
	}
	return &Logger{
		logger: log.New(w, "", log.LstdFlags),
		debug:  os.Getenv("BILIAUDIO_DEBUG") != "",
	}
}

// Info 输出普通日志。
func (l *Logger) Info(format string, args ...any) {
	l.logger.Printf("[INFO] "+format, args...)
}

// Error 输出错误日志。
func (l *Logger) Error(format string, args ...any) {
	l.logger.Printf("[ERROR] "+format, args...)
}

// Warn 输出警告日志。
func (l *Logger) Warn(format string, args ...any) {
	l.logger.Printf("[WARN] "+format, args...)
}

// Debug 仅在设置 BILIAUDIO_DEBUG 时输出。
func (l *Logger) Debug(format string, args ...any) {
	if !l.debug {
		return
	}
	l.logger.Printf("[DEBUG] "+format, args...)
}
