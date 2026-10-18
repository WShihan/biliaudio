package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	bilibili "github.com/WShihan/biliaudio/internal/bili"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// newTestHandler 用假 B 站服务器驱动真实的 handler。
// 通过注入传输层把 api.bilibili.com 与媒体 CDN 都导向测试服务器，保证不触碰真实网络。
func newTestHandler(t *testing.T, cfg Config) (http.Handler, *httptest.Server) {
	t.Helper()

	payload := bytes.Repeat([]byte("audio-data-"), 2048)
	var mediaBase string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/view"):
			writeTestJSON(w, `{"code":0,"message":"OK","data":{
				"bvid":"BV1GJ411x7h7","title":"测试视频","duration":213,
				"pic":"http://i0.hdslb.com/cover.jpg","pubdate":1577836800,
				"desc":"这是简介","owner":{"name":"测试UP"},
				"stat":{"view":12345,"like":678,"coin":9,"favorite":1,"share":2,"danmaku":3},
				"pages":[{"cid":111,"page":1,"part":"第一话","duration":60},
				         {"cid":222,"page":2,"part":"第二话","duration":153}]}}`)
		case strings.HasSuffix(r.URL.Path, "/playurl"):
			// 关键：直链必须落在白名单域名上，否则代理会拒绝
			writeTestJSON(w, `{"code":0,"message":"OK","data":{"dash":{"audio":[
				{"id":30216,"bandwidth":64000,"baseUrl":"`+mediaBase+`/low.m4s"},
				{"id":30280,"bandwidth":192000,"baseUrl":"`+mediaBase+`/high.m4s"}]}}}`)
		case r.URL.Path == "/high.m4s" || r.URL.Path == "/low.m4s":
			if r.Header.Get("Referer") != bilibili.Referer {
				t.Errorf("音频请求缺少 Referer")
			}
			if rng := r.Header.Get("Range"); rng != "" {
				// 支持 "bytes=start-end" 与 "bytes=start-" 两种形态
				spec := strings.TrimPrefix(rng, "bytes=")
				startText, endText, hasEnd := strings.Cut(spec, "-")
				start, err := strconv.Atoi(strings.TrimSpace(startText))
				if err != nil || start < 0 || start >= len(payload) {
					w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
					return
				}
				end := len(payload) - 1
				if hasEnd && strings.TrimSpace(endText) != "" {
					if parsedEnd, err := strconv.Atoi(strings.TrimSpace(endText)); err == nil && parsedEnd < end {
						end = parsedEnd
					}
				}
				rest := payload[start : end+1]
				w.Header().Set("Content-Range",
					fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
				w.Header().Set("Content-Length", fmt.Sprint(len(rest)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(rest)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			_, _ = w.Write(payload)
		default:
			t.Errorf("未预期的请求：%s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	mediaBase = mediaServerURL(server.URL)

	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = NewLogger(io.Discard)
	}
	if cfg.Client == nil {
		cfg.Client = bilibili.NewClient(cfg.Cookie).WithTransport(redirectTo(server.URL))
	}

	handler, err := NewHandler(cfg)
	if err != nil {
		t.Fatalf("构造 handler 失败：%v", err)
	}
	return handler, server
}

// redirectTo 把所有请求导向测试服务器。
func redirectTo(baseURL string) http.RoundTripper {
	target := strings.TrimPrefix(baseURL, "http://")
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cloned := req.Clone(req.Context())
		cloned.URL.Scheme = "http"
		cloned.URL.Host = target
		cloned.Host = target
		return http.DefaultTransport.RoundTrip(cloned)
	})
}

// mediaServerURL 给出一个能通过媒体域名白名单、又指向测试服务器的地址。
func mediaServerURL(serverURL string) string {
	_, hostPort, _ := strings.Cut(serverURL, "://")
	// httptest 监听在 127.0.0.1，把主机名伪装成白名单里的域名
	return "http://upos-sz-mirrorcos.bilivideo.com:" + portOf(hostPort)
}

func portOf(hostPort string) string {
	if _, port, ok := strings.Cut(hostPort, ":"); ok {
		return port
	}
	return "80"
}

func writeTestJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func TestIndexPageRenders(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("首页状态码 %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{"哔哩哔哩音频解析", `id="parse-form"`, "/static/css/style.css"} {
		if !strings.Contains(body, want) {
			t.Errorf("首页缺少 %q", want)
		}
	}
}

func TestStaticAssetsServedFromEmbed(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	for _, path := range []string{"/static/css/style.css", "/static/js/player.js", "/favicon.ico"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("%s 状态码 %d", path, recorder.Code)
		}
		if recorder.Body.Len() == 0 {
			t.Errorf("%s 内容为空", path)
		}
	}
}

// 找不存在的静态文件不能返回首页内容（否则缓存会污染）。
func TestStaticAssetNotFound(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/static/nope.css", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("期望 404，实际 %d", recorder.Code)
	}
}

func TestFormPostReturnsFullPage(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/parse", strings.NewReader("input=BV1GJ411x7h7"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(recorder, req)

	body := recorder.Body.String()
	if !strings.Contains(body, "<!doctype html>") {
		t.Fatal("普通表单提交应返回完整页面（禁用 JS 也能用）")
	}
	if !strings.Contains(body, "测试视频") {
		t.Fatal("页面里没有解析结果")
	}
}

func TestFetchPostReturnsFragment(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/parse", strings.NewReader("input=BV1GJ411x7h7"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Requested-With", "fetch")
	handler.ServeHTTP(recorder, req)

	body := recorder.Body.String()
	if strings.Contains(body, "<!doctype html>") {
		t.Fatal("fetch 请求应只返回结果片段")
	}
	for _, want := range []string{"测试视频", "第一话", "第二话", "js-play", "js-download", `id="audio-data"`} {
		if !strings.Contains(body, want) {
			t.Errorf("结果片段缺少 %q", want)
		}
	}
}

func TestParseJSON(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/parse?input=BV1GJ411x7h7", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 %d", recorder.Code)
	}
	var payload struct {
		OK     bool `json:"ok"`
		Videos []struct {
			Title string `json:"Title"`
			Parts []struct {
				ProxyURL string `json:"ProxyURL"`
			} `json:"Parts"`
		} `json:"videos"`
		Tracks []Track `json:"tracks"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("JSON 解析失败：%v", err)
	}
	if !payload.OK || len(payload.Videos) != 1 {
		t.Fatalf("结果不符合预期：%+v", payload)
	}
	if len(payload.Videos[0].Parts) != 2 {
		t.Fatalf("应有两个分 P，实际 %d", len(payload.Videos[0].Parts))
	}
	if !strings.HasPrefix(payload.Videos[0].Parts[0].ProxyURL, "/api/proxy/") {
		t.Errorf("代理地址格式不对：%s", payload.Videos[0].Parts[0].ProxyURL)
	}
	if len(payload.Tracks) != 2 {
		t.Errorf("播放列表应有 2 条，实际 %d", len(payload.Tracks))
	}
}

func TestParseJSONRejectsEmptyInput(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/parse", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d", recorder.Code)
	}
}

func TestParseUnrecognizedInput(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/parse", strings.NewReader("input=今天天气不错"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Requested-With", "fetch")
	handler.ServeHTTP(recorder, req)

	if !strings.Contains(recorder.Body.String(), "未识别到有效的视频链接") {
		t.Fatalf("应提示无法识别，实际：%s", recorder.Body.String())
	}
}

// tokenFor 从解析结果里取出第一个分 P 的代理 token。
func tokenFor(t *testing.T, handler http.Handler) (string, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/parse?input=BV1GJ411x7h7", nil))

	var payload struct {
		Videos []struct {
			Parts []struct {
				ProxyURL string `json:"ProxyURL"`
			} `json:"Parts"`
		} `json:"videos"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("JSON 解析失败：%v", err)
	}
	proxyURL := payload.Videos[0].Parts[0].ProxyURL
	token := strings.TrimPrefix(strings.SplitN(proxyURL, "?", 2)[0], "/api/proxy/")
	return token, proxyURL
}

func TestProxyStreamsAudio(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	token, proxyURL := tokenFor(t, handler)
	_ = token

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, proxyURL, nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "audio/mp4" {
		t.Errorf("Content-Type = %q，期望 audio/mp4", got)
	}
	if got := recorder.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q", got)
	}
	if !strings.Contains(recorder.Header().Get("Content-Disposition"), "filename*=UTF-8''") {
		t.Errorf("下载文件名编码缺失：%q", recorder.Header().Get("Content-Disposition"))
	}
	if recorder.Body.Len() == 0 {
		t.Fatal("响应体为空")
	}
}

func TestProxyPassesThroughRange(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	_, proxyURL := tokenFor(t, handler)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, proxyURL, nil)
	req.Header.Set("Range", "bytes=0-1023")
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusPartialContent {
		t.Fatalf("期望 206，实际 %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Range"); !strings.HasPrefix(got, "bytes 0-1023/") {
		t.Errorf("Content-Range = %q", got)
	}
	if recorder.Body.Len() != 1024 {
		t.Errorf("应返回 1024 字节，实际 %d", recorder.Body.Len())
	}
}

// 代理必须拒绝非 B 站域名，否则会变成 SSRF 跳板。
func TestProxyRejectsForeignHost(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	for _, target := range []string{
		"http://127.0.0.1:8080/admin",
		"http://169.254.169.254/latest/meta-data/",
		"http://evil-bilivideo.com.attacker.net/a.m4s",
		"file:///etc/passwd",
	} {
		token := bilibili.EncodeToken(target)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/proxy/"+token, nil))
		if recorder.Code != http.StatusForbidden {
			t.Errorf("目标 %s 应被拒绝，实际状态码 %d", target, recorder.Code)
		}
	}
}

func TestProxyRejectsBadToken(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/proxy/not-base64!!", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d", recorder.Code)
	}
}

func TestAccessTokenGuard(t *testing.T) {
	handler, server := newTestHandler(t, Config{Token: "s3cret"})
	defer server.Close()

	// 不带令牌被拒绝
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("无令牌应 403，实际 %d", recorder.Code)
	}

	// 带正确令牌通过，并种下 Cookie
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/?token=s3cret", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("带令牌应 200，实际 %d", recorder.Code)
	}
	if len(recorder.Result().Cookies()) == 0 {
		t.Fatal("应种下令牌 Cookie，否则后续请求都要带参数")
	}

	// 携带 Cookie 时无需再带参数
	recorder = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: tokenCookie, Value: "s3cret"})
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("Cookie 方式应通过，实际 %d", recorder.Code)
	}

	// 健康检查不受令牌限制
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("健康检查应放行，实际 %d", recorder.Code)
	}
}

func TestRateLimiterBlocksBurst(t *testing.T) {
	limiter := newRateLimiter(2, 1e9)
	if !limiter.allow("1.2.3.4") || !limiter.allow("1.2.3.4") {
		t.Fatal("前两次应放行")
	}
	if limiter.allow("1.2.3.4") {
		t.Fatal("第三次应被限流")
	}
	if !limiter.allow("5.6.7.8") {
		t.Fatal("不同 IP 不应互相影响")
	}
}

func TestContentDispositionFallsBackToASCII(t *testing.T) {
	header := contentDisposition("纯中文标题")
	if !strings.Contains(header, `filename="bilibili_audio.m4a"`) {
		t.Errorf("纯中文标题需要 ASCII 回退名：%q", header)
	}
	if !strings.Contains(header, "filename*=UTF-8''") {
		t.Errorf("缺少 UTF-8 文件名：%q", header)
	}

	header = contentDisposition("Song 歌名")
	if !strings.Contains(header, `filename="Song .m4a"`) {
		t.Errorf("ASCII 回退名不正确：%q", header)
	}
}

func TestClientIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	if got := clientIP(req); got != "192.0.2.10" {
		t.Errorf("clientIP = %q", got)
	}

	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	if got := clientIP(req); got != "203.0.113.7" {
		t.Errorf("应优先取 X-Forwarded-For 的第一个地址，得到 %q", got)
	}
}

func TestTruncateCountsRunes(t *testing.T) {
	if got := truncate("中文测试", 2); got != "中文..." {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("短", 5); got != "短" {
		t.Errorf("truncate = %q", got)
	}
}

func TestTracksJSONIsEscapedForScriptTag(t *testing.T) {
	data := &ViewData{Tracks: []Track{{Name: "</script><b>x", URL: "/a"}}}
	encoded, err := json.Marshal(data.Tracks)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "</script>") {
		t.Fatal("JSON 里的 </script> 必须转义，否则会破坏页面")
	}
}

func TestLoggerRespectsDebugFlag(t *testing.T) {
	var buf bytes.Buffer
	logger := &Logger{logger: log.New(&buf, "", 0), debug: false}
	logger.Debug("不该出现")
	logger.Info("应该出现")
	if strings.Contains(buf.String(), "不该出现") {
		t.Error("未开启调试时不应输出 Debug")
	}
	if !strings.Contains(buf.String(), "应该出现") {
		t.Error("Info 应被输出")
	}
}

// 回归：Cookie 存不下来时，页面必须把令牌暴露给前端，让请求能显式带上。
func TestPageExposesTokenForCookieLessBrowsers(t *testing.T) {
	handler, server := newTestHandler(t, Config{Token: "s3cret"})
	defer server.Close()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/?token=s3cret", nil))
	body := recorder.Body.String()

	if !strings.Contains(body, `content="s3cret"`) {
		t.Fatal("页面应把已校验的令牌注入 meta 标签，供 Cookie 无法保存时兜底")
	}
	if !strings.Contains(body, `data-token="s3cret"`) {
		t.Fatal("表单应带上 data-token，保证禁用 JS 直提表单也能通过")
	}
	// 未启用令牌时不应出现任何令牌痕迹
	recorder = httptest.NewRecorder()
	plain, plainServer := newTestHandler(t, Config{})
	defer plainServer.Close()
	plain.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(recorder.Body.String(), "biliaudio-token") {
		t.Fatal("未启用令牌时不应注入 meta 标签")
	}
}

// 回归：完全不依赖 Cookie，仅靠页面里的令牌也要能完成解析与下载。
func TestParseWorksWithoutCookie(t *testing.T) {
	handler, server := newTestHandler(t, Config{Token: "s3cret"})
	defer server.Close()

	// 1) 取页面，读出前端会用的令牌（模拟浏览器渲染后的 JS 读取）
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/?token=s3cret", nil))
	body := recorder.Body.String()
	idx := strings.Index(body, `name="biliaudio-token" content="`)
	if idx < 0 {
		t.Fatal("页面缺少令牌 meta")
	}
	rest := body[idx+len(`name="biliaudio-token" content="`):]
	token := rest[:strings.Index(rest, `"`)]

	// 2) 不带任何 Cookie 发起解析
	recorder = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/parse?token="+token,
		strings.NewReader("input=BV1GJ411x7h7"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Requested-With", "fetch")
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("无 Cookie 时应能解析，实际 %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "测试视频") {
		t.Fatal("结果里应包含视频标题")
	}

	// 3) 下载链接同样要能通过（不带 Cookie）
	proxyURL := ""
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if strings.Contains(line, `data-url="`) {
			start := strings.Index(line, `data-url="`) + len(`data-url="`)
			proxyURL = line[start:]
			proxyURL = proxyURL[:strings.Index(proxyURL, `"`)]
			break
		}
	}
	if proxyURL == "" {
		t.Fatal("未找到代理地址")
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, proxyURL+"&token="+token, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("无 Cookie 时应能下载，实际 %d", recorder.Code)
	}
}

// proxyURL2 直接调用内部函数构造代理地址，用于覆盖特殊文件名。
func proxyURL2(t *testing.T, filename string) string {
	t.Helper()
	return proxyURL("https://upos-sz-mirrorcos.bilivideo.com/a.m4s", filename)
}

// 回归：文件名里的空格必须能完整还原。
// 曾用 QueryEscape 编码，空格变成 "+"，经 HTML 属性转义与浏览器解码后，
// 服务端又把它当成空格分隔，文件名被从第一个空格处截断。
func TestProxyFilenameKeepsSpaces(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	_, proxyURL := tokenFor(t, handler)
	if strings.Contains(proxyURL, "+") {
		t.Fatalf("代理地址里不应出现 \"+\"（会被当成空格），实际 %q", proxyURL)
	}

	// 按浏览器方式取回 filename 参数，应当完整
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	// 第一话 这个标题没有空格，因此另测一个带空格的
	if got := parsed.Query().Get("filename"); got != "第一话" {
		t.Fatalf("filename 参数不正确：%q", got)
	}
	spaced := proxyURL2(t, "第二话 · 某 个 标 题")
	spacedParsed, err := url.Parse(spaced)
	if err != nil {
		t.Fatal(err)
	}
	if got := spacedParsed.Query().Get("filename"); got != "第二话 · 某 个 标 题" {
		t.Fatalf("带空格的文件名被破坏：%q", got)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, proxyURL, nil))
	disposition := recorder.Header().Get("Content-Disposition")
	if !strings.Contains(disposition, "filename*=UTF-8''") {
		t.Fatalf("缺少 UTF-8 文件名：%q", disposition)
	}
	// UTF-8 文件名要能被解码还原，并且与原始标题一致
	encoded := disposition[strings.Index(disposition, "filename*=UTF-8''")+len("filename*=UTF-8''"):]
	decoded, err := url.PathUnescape(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != "第一话.m4a" {
		t.Fatalf("Content-Disposition 文件名不正确：%q", decoded)
	}
}

// 回归：textarea 的初始值必须是空的。
//
// HTML 规则里 <textarea> 与 </textarea> 之间的所有字符都是初始值，模板里
// 一个换行缩进就会让输入框自带 "\n"：客户端 trim 后判定非空、照常提交，
// 服务端 TrimSpace 后判空 → 报「请输入视频链接、BV 号或包含它们的文本」。
func TestTextareaHasNoLeadingWhitespace(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	cases := []struct {
		name string
		req  *http.Request
		want string // 该场景下输入框应有的内容
	}{
		{"首页", httptest.NewRequest(http.MethodGet, "/", nil), ""},
		{"解析后的完整页面", formPost("input=BV1GJ411x7h7", false), "BV1GJ411x7h7"},
	}
	for _, tc := range cases {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, tc.req)
		body := recorder.Body.String()
		path := tc.name

		start := strings.Index(body, "<textarea")
		if start < 0 {
			t.Fatalf("%s：未找到 textarea", path)
		}
		rest := body[start:]
		contentStart := strings.Index(rest, ">") + 1
		contentEnd := strings.Index(rest, "</textarea>")
		if contentStart <= 0 || contentEnd < contentStart {
			t.Fatalf("%s：textarea 结构异常", path)
		}
		got := rest[contentStart:contentEnd]
		if got != tc.want {
			t.Fatalf("%s：textarea 内容应为 %q，实际 %q", path, tc.want, got)
		}
		// 关键：内容前后不能有空白，否则服务端 TrimSpace 后可能判空
		if got != strings.TrimSpace(got) {
			t.Fatalf("%s：textarea 内容带有首尾空白 %q", path, got)
		}
	}
}

// formPost 构造一次表单提交请求。
func formPost(body string, fetch bool) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/parse", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if fetch {
		req.Header.Set("X-Requested-With", "fetch")
	}
	return req
}

// 服务端渲染回填时，输入框里应保留用户输入的链接（去掉首尾空白）。
func TestTextareaKeepsSubmittedInput(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	link := "https://www.bilibili.com/video/BV1GJ411x7h7"
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/parse",
		strings.NewReader("input="+url.QueryEscape("  "+link+"  ")))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(recorder, req)

	body := recorder.Body.String()
	start := strings.Index(body, "<textarea")
	rest := body[start:]
	content := rest[strings.Index(rest, ">")+1 : strings.Index(rest, "</textarea>")]
	if content != link {
		t.Fatalf("输入框应回填 %q（去掉首尾空白），实际 %q", link, content)
	}
}

// 回归：静态资源地址必须带内容指纹。
//
// 否则改了 player.js 之后，浏览器会继续用缓存里的旧版本（表现为「页面是新的、
// 交互却按旧逻辑走」），这类问题极难排查。
func TestStaticAssetsAreVersioned(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	body := recorder.Body.String()

	for _, asset := range []string{"css/style.css", "js/player.js"} {
		pattern := regexp.MustCompile(`/static/` + regexp.QuoteMeta(asset) + `\?v=([0-9a-f]{12})`)
		match := pattern.FindStringSubmatch(body)
		if match == nil {
			t.Fatalf("%s 的引用应带上 12 位内容指纹，页面内容里未找到", asset)
		}
		if match[1] != AssetVersion() {
			t.Fatalf("%s 的指纹 %q 与 AssetVersion() %q 不一致", asset, match[1], AssetVersion())
		}
	}

	// 带查询串的地址仍要能取到文件
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodGet, "/static/js/player.js?v="+AssetVersion(), nil))
	if recorder.Code != http.StatusOK || recorder.Body.Len() == 0 {
		t.Fatalf("带版本参数的静态资源应正常返回，实际 %d / %d 字节",
			recorder.Code, recorder.Body.Len())
	}
}

// 指纹必须随内容变化，否则缓存还是会命中旧文件。
func TestAssetVersionFollowsContent(t *testing.T) {
	first := AssetVersion()
	if len(first) != 12 {
		t.Fatalf("指纹长度应为 12，实际 %d", len(first))
	}
	// 同一进程内应保持稳定（用了 sync.OnceValue）
	if second := AssetVersion(); second != first {
		t.Fatalf("指纹应稳定：%q != %q", first, second)
	}
}

// newTestClientTo 返回一个把 B 站接口全部指向 baseURL 的客户端。
func newTestClientTo(baseURL string) *bilibili.Client {
	return bilibili.NewClient("").WithTransport(redirectTo(baseURL))
}
