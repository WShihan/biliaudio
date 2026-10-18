package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	bilibili "github.com/WShihan/biliaudio/internal/bili"
)

const (
	// 转发给浏览器的上游响应头；其余（如 content-encoding）交给 Go 自行处理
	contentTypeHeader   = "Content-Type"
	contentLengthHeader = "Content-Length"
	chunkSize           = 64 << 10
	streamSemaphoreSize = 8
)

// proxy 负责把 B 站音频流转发给浏览器。
//
// 手机浏览器会用 Range 请求拖动进度，因此必须原样透传 Range/Content-Range，
// 不能把整个文件先读进内存。
type proxy struct {
	client *bilibili.Client
	logger *Logger
	// 限制并发转发数，避免单个实例被拖垮
	sem chan struct{}
}

func newProxy(client *bilibili.Client, logger *Logger) *proxy {
	return &proxy{
		client: client,
		logger: logger,
		sem:    make(chan struct{}, streamSemaphoreSize),
	}
}

func (p *proxy) handle(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	streamURL, err := bilibili.DecodeToken(token)
	if err != nil {
		http.Error(w, "音频地址无效", http.StatusBadRequest)
		return
	}
	// 只放行 B 站自家域名，否则这里会变成 SSRF 跳板
	if err := bilibili.CheckMediaURL(streamURL); err != nil {
		p.logger.Warn("拒绝代理非白名单地址: %v", err)
		http.Error(w, "音频地址不被允许", http.StatusForbidden)
		return
	}

	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-r.Context().Done():
		return
	case <-time.After(15 * time.Second):
		http.Error(w, "服务繁忙，请稍后重试", http.StatusServiceUnavailable)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, streamURL, nil)
	if err != nil {
		http.Error(w, "构造上游请求失败", http.StatusInternalServerError)
		return
	}
	// 先铺上访问 B 站所需的请求头，再叠加 Range；
	// 注意不能整体赋值，否则前面设置的 Range/If-Range 会被覆盖掉
	for key, values := range p.client.Headers() {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if rng := r.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}
	if ifRange := r.Header.Get("If-Range"); ifRange != "" {
		req.Header.Set("If-Range", ifRange)
	}

	resp, err := p.streamClient().Do(req)
	if err != nil {
		p.logger.Warn("拉取音频流失败: %v", err)
		http.Error(w, "拉取音频流失败", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	header := w.Header()
	copyHeader(header, resp.Header, contentTypeHeader, contentLengthHeader,
		"Content-Range", "Accept-Ranges", "ETag", "Last-Modified")
	// 部分 CDN 会把 m4s 标成 octet-stream，浏览器据此无法直接试听
	if !strings.HasPrefix(header.Get(contentTypeHeader), "audio/") {
		header.Set(contentTypeHeader, guessContentType(streamURL))
	}
	if header.Get("Accept-Ranges") == "" {
		header.Set("Accept-Ranges", "bytes")
	}
	// 直链带 deadline 签名，缓存过久会导致后续播放 403
	header.Set("Cache-Control", "private, max-age=600")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Disposition", contentDisposition(r.URL.Query().Get("filename")))

	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.CopyBuffer(w, resp.Body, make([]byte, chunkSize)); err != nil {
		// 客户端主动断开（拖动进度条）会走到这里，不必当成错误
		p.logger.Debug("转发中断: %v", err)
	}
}

// streamClient 使用独立的客户端处理流式响应：不设整体超时，
// 但要求连接与响应头阶段有超时，避免上游挂死。
func (p *proxy) streamClient() *http.Client {
	transport := p.client.Transport
	if transport == nil {
		transport = sharedTransport
	}
	return &http.Client{
		Transport: transport,
		Timeout:   0,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

func copyHeader(dst, src http.Header, keys ...string) {
	for _, key := range keys {
		if value := src.Get(key); value != "" {
			dst.Set(key, value)
		}
	}
}

// guessContentType 依据扩展名推断音频类型，B 站 DASH 音频统一是 fMP4。
func guessContentType(rawURL string) string {
	path := rawURL
	if parsed, err := url.Parse(rawURL); err == nil {
		path = parsed.Path
	}
	switch {
	case strings.HasSuffix(path, ".flac"):
		return "audio/flac"
	case strings.HasSuffix(path, ".mp3"):
		return "audio/mpeg"
	default:
		return "audio/mp4"
	}
}

// contentDisposition 同时给出 ASCII 回退名与 RFC 5987 编码的 UTF-8 文件名。
func contentDisposition(filename string) string {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		filename = "bilibili_audio"
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".m4a") {
		filename += ".m4a"
	}
	return `inline; filename="` + asciiFallback(filename) + `"; filename*=UTF-8''` +
		url.PathEscape(filename)
}

// asciiFallback 生成只含 ASCII 的回退文件名。
func asciiFallback(name string) string {
	var builder strings.Builder
	for _, r := range name {
		if r < 128 {
			builder.WriteRune(r)
		}
	}
	cleaned := strings.Trim(builder.String(), " ._-")
	if cleaned == "" || !strings.Contains(cleaned, ".") {
		return "bilibili_audio.m4a"
	}
	return cleaned
}
