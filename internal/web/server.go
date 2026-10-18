// Package web 把解析与下载能力包装成 HTTP 服务。
//
// 静态资源（模板、CSS、JS、图标）通过 embed 编进二进制，因此部署时只需要
// 拷贝一个可执行文件到 VPS 上，不用额外维护目录结构。
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/WShihan/biliaudio/internal/bili"
	"github.com/WShihan/biliaudio/internal/buildinfo"
)

//go:embed assets
var assets embed.FS

// MaxVideos 单次请求最多解析的视频数，避免一个请求打爆接口配额。
const MaxVideos = 10

// maxFormBody 限制兜底读取的表单体大小，避免超大请求占内存。
const maxFormBody = 1 << 20

// Config 是启动 Web 服务所需的配置。
type Config struct {
	Addr    string        // 监听地址，如 ":8080"
	Cookie  string        // B 站登录态，可为空
	Token   string        // 访问令牌；为空表示不校验
	Timeout time.Duration // 解析与下载的整体超时
	Logger  *Logger
	// Client 可选，用于注入自定义传输层（测试或代理出网）；为空时按 Cookie 新建
	Client *bili.Client
}

// ViewVideo 是模板需要的视频信息。
type ViewVideo struct {
	Bvid        string
	Title       string
	Author      string
	Cover       string
	Description string
	Duration    string
	Pubdate     string
	Views       string
	Likes       string
	Coins       string
	Favorites   string
	Shares      string
	Danmaku     string
	Parts       []ViewPart
}

// ViewPart 是一个可播放/下载的分段。
type ViewPart struct {
	Name      string
	TrackName string
	Duration  string
	Filename  string
	ProxyURL  string
}

// Track 是底部播放器需要的曲目信息。
type Track struct {
	Name   string `json:"name"`
	Artist string `json:"artist"`
	URL    string `json:"url"`
	Cover  string `json:"cover"`
}

// ViewData 是模板渲染所需的数据。
type ViewData struct {
	// Token 是已校验的访问令牌。页面会把它注入 meta 标签，前端据此显式携带，
	// 作为 Cookie 无法保存时的兜底。
	Token string
	// AssetVersion 是静态资源指纹，拼进 CSS/JS 地址，避免浏览器用旧缓存
	AssetVersion string
	Input        string
	Error        string
	Failures     []string
	Videos       []ViewVideo
	Tracks       []Track
	TracksJSON   template.JS
}

// NewViewData 构造空视图（首页）。
func NewViewData() *ViewData {
	return &ViewData{
		TracksJSON:   template.JS("[]"),
		AssetVersion: AssetVersion(),
	}
}

// prepare 补齐模板需要的派生字段。
func (v *ViewData) prepare() {
	if v.TracksJSON == "" || v.TracksJSON == "null" {
		v.TracksJSON = template.JS("[]")
	}
}

// assetVersion 是静态资源的整体指纹（前 12 位 SHA-256）。
//
// 页面引用静态资源时带上它：内容一变 URL 就变，浏览器必然重新获取。
// 否则会出现「服务端已更新、浏览器仍用旧的 player.js」——
// 表现为页面看着是新的、交互却按旧逻辑走，极难排查。
var assetVersion = sync.OnceValue(func() string {
	hash := sha256.New()
	for _, name := range []string{"css/style.css", "js/player.js", "favicon.ico"} {
		content, err := fs.ReadFile(Assets(), name)
		if err != nil {
			continue
		}
		_, _ = hash.Write([]byte(name))
		_, _ = hash.Write(content)
	}
	return hex.EncodeToString(hash.Sum(nil))[:12]
})

// AssetVersion 暴露静态资源指纹，供模板拼接带版本的地址。
func AssetVersion() string { return assetVersion() }

// TemplateFingerprint 返回模板内容的短指纹（前 8 位 SHA-256）。
//
// 用途：`go run` 会把服务跑在一个常驻的临时二进制里，很容易出现「改了代码但
// 端口还被旧进程占着」而误判为修改无效。把指纹打印在启动横幅上，
// 与源码对比即可确认实际运行的是哪一版。
func TemplateFingerprint() string {
	content, err := fs.ReadFile(Assets(), "templates/index.html")
	if err != nil {
		return "unknown"
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])[:8]
}

// TextareaIsClean 校验渲染出的完整页面里，输入框初始值是否为空白。
//
// 这是曾经的线上问题：`<textarea>` 与 `</textarea>` 之间的换行会成为输入框
// 初始值，使前端「非空校验」永远通过。启动时自检一次，避免再次带病上线。
func TextareaIsClean() error {
	tmpl, err := parseTemplates()
	if err != nil {
		return err
	}
	var buffer bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buffer, "page", NewViewData()); err != nil {
		return err
	}
	body := buffer.String()
	start := strings.Index(body, "<textarea")
	if start < 0 {
		return errors.New("页面里找不到 textarea")
	}
	rest := body[start:]
	contentStart := strings.Index(rest, ">") + 1
	contentEnd := strings.Index(rest, "</textarea>")
	if contentStart <= 0 || contentEnd < contentStart {
		return errors.New("textarea 结构异常")
	}
	if value := rest[contentStart:contentEnd]; value != "" {
		return fmt.Errorf("输入框初始值应为空，实际为 %q", value)
	}
	return nil
}

// Assets 返回内嵌的静态资源文件系统。
func Assets() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		// assets 是编译期常量，出错说明 embed 指令被破坏
		panic(err)
	}
	return sub
}

// NewHandler 组装所有路由。返回的 handler 可直接交给 http.Server。
func NewHandler(cfg Config) (http.Handler, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Minute
	}
	if cfg.Logger == nil {
		cfg.Logger = NewLogger(nil)
	}

	tmpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}

	client := cfg.Client
	if client == nil {
		client = bili.NewClient(cfg.Cookie)
	}
	limiter := newRateLimiter(30, time.Minute)
	proxy := newProxy(client, cfg.Logger)

	mux := http.NewServeMux()
	mux.Handle("GET /static/", cacheControl(
		http.StripPrefix("/static/", http.FileServer(http.FS(Assets()))),
		"public, max-age=604800, immutable",
	))
	mux.HandleFunc("GET /favicon.ico", serveAsset("favicon.ico"))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		data := NewViewData()
		// 支持直接以链接形式带上输入内容，便于复现与排查
		data.Input = strings.TrimSpace(r.URL.Query().Get("input"))
		renderPage(w, r, tmpl, data)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]any{"ok": true}
		for key, value := range buildinfo.Health() {
			payload[key] = value
		}
		writeJSON(w, http.StatusOK, payload)
	})
	mux.HandleFunc("POST /api/parse", parseHandler(tmpl, client, cfg, limiter))
	mux.HandleFunc("GET /api/parse", parseJSONHandler(client, cfg, limiter))
	// 诊断端点：把浏览器实际发来的请求原样回显。
	// 用于区分「请求没带 input」「Content-Type 不对」「表单解析失败」等情况。
	// 默认关闭：它会回显请求内容，长期暴露在公网并不合适。
	if os.Getenv("BILIAUDIO_ECHO") != "" {
		// GET 与 POST 都要能看：GET 用于直接在地址栏打开，POST 用于模拟表单
		mux.HandleFunc("GET /api/debug/echo", echoHandler(cfg))
		mux.HandleFunc("POST /api/debug/echo", echoHandler(cfg))
	}
	// Range 请求用 GET；HEAD 用于让浏览器探测是否支持断点续传
	mux.HandleFunc("GET /api/proxy/{token}", proxy.handle)
	mux.HandleFunc("HEAD /api/proxy/{token}", proxy.handle)

	return guard(cfg, mux), nil
}

// parseHandler 处理表单提交：带 X-Requested-With 时只回结果片段。
func parseHandler(
	tmpl *template.Template,
	client *bili.Client,
	cfg Config,
	limiter *rateLimiter,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow(clientIP(r)) {
			http.Error(w, "请求过于频繁，请稍后再试", http.StatusTooManyRequests)
			return
		}
		contentType := r.Header.Get("Content-Type")
		bodyLen := r.ContentLength
		if err := parseFormLenient(r); err != nil {
			cfg.Logger.Error("[parse] 表单解析失败: content-type=%q bodyLen=%d err=%v",
				contentType, bodyLen, err)
			http.Error(w, "表单解析失败", http.StatusBadRequest)
			return
		}

		text := trimSpace(r.FormValue("input"))
		cfg.Logger.Info("[parse] content-type=%q bodyLen=%d text=%q len=%d post=%v query=%v keys=%v",
			contentType, bodyLen, truncate(text, 60), len(text), r.PostForm, r.URL.Query(), formKeys(r))
		if text == "" {
			data := NewViewData()
			data.Error = "请输入视频链接、BV 号或包含它们的文本"
			renderResults(w, r, tmpl, data)
			return
		}

		data := collect(r, client, cfg, text)
		renderResults(w, r, tmpl, data)
	}
}

// parseJSONHandler 给脚本调用的 JSON 接口。
func parseJSONHandler(client *bili.Client, cfg Config, limiter *rateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow(clientIP(r)) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"ok": false, "error": "请求过于频繁，请稍后再试",
			})
			return
		}

		text := trimSpace(r.URL.Query().Get("input"))
		if text == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok": false, "error": "缺少 input 参数",
			})
			return
		}

		data := collect(r, client, cfg, text)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       len(data.Videos) > 0,
			"error":    data.Error,
			"failures": data.Failures,
			"videos":   data.Videos,
			"tracks":   data.Tracks,
		})
	}
}

// guard 施加可选的访问令牌校验。
func guard(cfg Config, next http.Handler) http.Handler {
	if cfg.Token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 健康检查与静态资源不校验令牌：前者留给探活工具，
		// 后者不含敏感信息，且浏览器加载 CSS/JS 时不会带上 ?token=，
		// 若一并拦住会导致页面没有任何样式。
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/static/") ||
			r.URL.Path == "/favicon.ico" {
			next.ServeHTTP(w, r)
			return
		}
		if !validToken(r, cfg.Token) {
			http.Error(w, strings.Join([]string{
				"访问令牌无效。",
				"",
				"请用启动横幅里打印的那个令牌访问，例如：",
				"    http://<地址>:<端口>/?token=<令牌>",
				"",
				"注意：启动时如果没有加 -token，服务每次启动都会随机生成一个新令牌，",
				"旧地址会随之失效。想固定下来就加：-token <你自己的令牌>",
			}, "\n"), http.StatusForbidden)
			return
		}
		// 种下 Cookie 方便后续请求；但**不能只依赖它**——部分浏览器/隐私模式
		// 在 http 或局域网地址下不会保存 Cookie，所以同时把令牌注入页面，
		// 由前端显式带上（见 templates/index.html 的 meta 标签）。
		if cookie, err := r.Cookie(tokenCookie); err != nil || cookie.Value != cfg.Token {
			http.SetCookie(w, &http.Cookie{
				Name:     tokenCookie,
				Value:    cfg.Token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   30 * 24 * 3600,
			})
		}
		ctx := context.WithValue(r.Context(), tokenContextKey{}, cfg.Token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// tokenContextKey 用于把「已校验的令牌」从守卫传给模板。
type tokenContextKey struct{}

// tokenFromContext 取回已校验的令牌；未启用令牌校验时返回空串。
func tokenFromContext(ctx context.Context) string {
	if value, ok := ctx.Value(tokenContextKey{}).(string); ok {
		return value
	}
	return ""
}

func validToken(r *http.Request, want string) bool {
	if got := r.URL.Query().Get("token"); got != "" && secureEqual(got, want) {
		return true
	}
	if got := r.Header.Get("X-Auth-Token"); got != "" && secureEqual(got, want) {
		return true
	}
	if cookie, err := r.Cookie(tokenCookie); err == nil && secureEqual(cookie.Value, want) {
		return true
	}
	return false
}

// renderPage 输出完整页面。
func renderPage(w http.ResponseWriter, r *http.Request, tmpl *template.Template, data *ViewData) {
	data.prepare()
	data.Token = tokenFromContext(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := tmpl.ExecuteTemplate(w, "page", data); err != nil {
		http.Error(w, "渲染页面失败", http.StatusInternalServerError)
	}
}

// renderResults 输出结果；脚本发起的请求只要片段，其余回完整页面，
// 这样禁用 JS 时表单直接提交也能正常使用。
func renderResults(w http.ResponseWriter, r *http.Request, tmpl *template.Template, data *ViewData) {
	data.prepare()
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("X-Requested-With") != "fetch" {
		renderPage(w, r, tmpl, data)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "results", data); err != nil {
		http.Error(w, "渲染结果失败", http.StatusInternalServerError)
	}
}

// parseFormLenient 解析表单，并在 Content-Type 缺失或非表单类型时兜底。
//
// 关键背景：Go 的 r.ParseForm() 只在 Content-Type 为
// application/x-www-form-urlencoded 或 multipart/form-data 时解析 body；
// 其它情况（包括**完全没有 Content-Type**）会直接忽略 body。
// 某些客户端、反向代理或安全软件会丢掉这个头，于是出现
// 「浏览器 Network 面板里 input 明明有值，服务端却收到空串」的诡异现象。
// 这里在那种情况下手动按表单读取 body，并与查询串合并。
func parseFormLenient(r *http.Request) error {
	contentType := r.Header.Get("Content-Type")
	lower := strings.ToLower(strings.TrimSpace(contentType))
	isForm := strings.HasPrefix(lower, "application/x-www-form-urlencoded") ||
		strings.HasPrefix(lower, "multipart/form-data")

	if isForm || r.Body == nil {
		return r.ParseForm()
	}

	// 先把查询串解析好，保证 ?input= 这类请求仍然可用
	if err := r.ParseForm(); err != nil {
		return err
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch {
		return nil
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxFormBody))
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	values, err := url.ParseQuery(string(raw))
	if err != nil {
		// 真的不是表单内容（比如 JSON），交给调用方按「无法识别」处理
		return nil
	}
	for key, items := range values {
		r.Form[key] = append(r.Form[key], items...)
		r.PostForm[key] = append(r.PostForm[key], items...)
	}
	return nil
}

// formKeys 返回表单字段名，便于日志排查。
func formKeys(r *http.Request) []string {
	keys := make([]string, 0, len(r.Form))
	for key := range r.Form {
		keys = append(keys, key)
	}
	return keys
}

// writeJSON 输出 JSON。
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// serveAsset 从内嵌资源里取单个文件。
func serveAsset(name string) http.HandlerFunc {
	content, err := fs.ReadFile(Assets(), name)
	if err != nil {
		panic(fmt.Sprintf("内嵌资源缺失: %s", name))
	}
	contentType := "application/octet-stream"
	if name == "favicon.ico" {
		contentType = "image/x-ico"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(content)
	}
}

func cacheControl(next http.Handler, value string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", value)
		next.ServeHTTP(w, r)
	})
}
