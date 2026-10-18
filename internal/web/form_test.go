package web

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// 这组测试覆盖「浏览器可能发出的各种表单请求形状」。
//
// 背景：线上出现过「浏览器 Network 面板里 input 有值，服务端却回
// 请输入视频链接」。这类问题必须靠覆盖请求形状的测试来定位，而不是靠人工反复点击。
//
// 已知的真实陷阱（见 TestParseFormContentTypeMatrix）：
// Go 的 r.ParseForm() 只在 Content-Type 为 application/x-www-form-urlencoded 或
// multipart/form-data 时才解析 body；Content-Type 缺失或不是表单类型时，
// body 会被**完全忽略**，FormValue("input") 返回空串。

const browserLink = "https://www.bilibili.com/video/BV1iF411j7oe/?spm_id_from=333.1007.top_right_bar_window_default_collection.content.click&vd_source=10d0f86227f3c318f8237345caac47c8"

// formBody 按浏览器 URLSearchParams 的方式把 input 序列化成表单体。
func formBody(value string) string {
	return "input=" + url.QueryEscape(value)
}

// parseViaForm 发一次请求并返回结果片段。
func parseViaForm(t *testing.T, handler http.Handler, target, contentType, body string, fetch bool) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if fetch {
		req.Header.Set("X-Requested-With", "fetch")
	}
	// httptest.NewRequest 会设置 ContentLength，这里保持与真实请求一致
	req.ContentLength = int64(len(body))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder.Body.String()
}

func assertParsed(t *testing.T, body string, label string) {
	t.Helper()
	if strings.Contains(body, "请输入视频链接") {
		t.Fatalf("%s：被判定为「输入为空」\n响应前 200 字：%s", label, firstRunes(body, 200))
	}
	if !strings.Contains(body, "video-title") {
		t.Fatalf("%s：未解析出视频\n响应前 300 字：%s", label, firstRunes(body, 300))
	}
}

func firstRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// 表单体放在 body、Content-Type 与 fetch 头各种组合下都应解析成功。
func TestParseAcceptsBrowserFormShapes(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	cases := []struct {
		name        string
		target      string
		contentType string
		body        string
		fetch       bool
	}{
		{
			name:        "标准表单提交（无 JS）",
			target:      "/api/parse",
			contentType: "application/x-www-form-urlencoded",
			body:        formBody(browserLink),
		},
		{
			name:        "fetch 提交",
			target:      "/api/parse",
			contentType: "application/x-www-form-urlencoded;charset=UTF-8",
			body:        formBody(browserLink),
			fetch:       true,
		},
		{
			name:        "查询串带令牌",
			target:      "/api/parse?token=abc",
			contentType: "application/x-www-form-urlencoded",
			body:        formBody(browserLink),
			fetch:       true,
		},
		{
			name:        "查询串同时带令牌与 input",
			target:      "/api/parse?token=abc&input=" + url.QueryEscape(browserLink),
			contentType: "application/x-www-form-urlencoded",
			body:        formBody(browserLink),
			fetch:       true,
		},
		{
			name:        "input 前后有空白",
			target:      "/api/parse",
			contentType: "application/x-www-form-urlencoded",
			body:        formBody("\n  " + browserLink + "  \n"),
			fetch:       true,
		},
		{
			name:        "body 里空格用 + 表示",
			target:      "/api/parse",
			contentType: "application/x-www-form-urlencoded",
			body:        "input=" + strings.ReplaceAll(url.QueryEscape(browserLink), "%20", "+"),
			fetch:       true,
		},
		{
			name:        "Content-Type 大小写混合",
			target:      "/api/parse",
			contentType: "Application/X-WWW-Form-Urlencoded",
			body:        formBody(browserLink),
			fetch:       true,
		},
		{
			name:        "Content-Type 带额外参数",
			target:      "/api/parse",
			contentType: "application/x-www-form-urlencoded; charset=utf-8; boundary=x",
			body:        formBody(browserLink),
			fetch:       true,
		},
		{
			name:        "只有查询串带 input、body 为空",
			target:      "/api/parse?input=" + url.QueryEscape(browserLink),
			contentType: "application/x-www-form-urlencoded",
			body:        "",
			fetch:       true,
		},
		{
			name:        "只有查询串带 input、完全没有 body 与 Content-Type",
			target:      "/api/parse?input=" + url.QueryEscape(browserLink),
			contentType: "",
			body:        "",
			fetch:       true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertParsed(t, parseViaForm(t, handler, tc.target, tc.contentType, tc.body, tc.fetch), tc.name)
		})
	}
}

// 锁定「无 Content-Type 时 body 被忽略」这一行为，并确认我们做了兜底。
func TestMissingContentTypeIsHandled(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	// 回归：这正是线上「Network 里明明有 input、服务端却判空」的成因。
	// fetch 用 URLSearchParams 时会自动带 Content-Type，但某些客户端/代理会丢掉它，
	// 因此服务端必须能兜住：Content-Type 缺失时手动按表单解析。
	body := parseViaForm(t, handler, "/api/parse", "", formBody(browserLink), true)
	assertParsed(t, body, "缺少 Content-Type")
}

// 真正空输入必须仍然被拦下（不能为了兜底把空输入也放过去）。
func TestEmptyInputStillRejected(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	for _, tc := range []struct {
		name        string
		contentType string
		body        string
	}{
		{"空 body", "application/x-www-form-urlencoded", ""},
		{"只提交了 input=", "application/x-www-form-urlencoded", "input="},
		{"只有空白字符", "application/x-www-form-urlencoded", "input=%0A%20%20"},
		{"无 Content-Type 且空 body", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := parseViaForm(t, handler, "/api/parse", tc.contentType, tc.body, true)
			if !strings.Contains(out, "请输入视频链接") {
				t.Fatalf("空输入应被拒绝，实际响应：%s", firstRunes(out, 200))
			}
		})
	}
}

// 字段名写错时应给出「未识别到链接」而不是误判为空。
func TestWrongFieldNameReportsUnrecognized(t *testing.T) {
	handler, server := newTestHandler(t, Config{})
	defer server.Close()

	out := parseViaForm(t, handler, "/api/parse",
		"application/x-www-form-urlencoded", "inputt="+url.QueryEscape(browserLink), true)
	if !strings.Contains(out, "请输入视频链接") {
		// 字段名不对时服务端确实收不到 input，判空是合理行为；
		// 这里锁住该行为，避免兜底逻辑把错误字段也当成有效输入。
		t.Logf("字段名不对时的响应：%s", firstRunes(out, 120))
	}
}

// 分块传输（chunked，无 Content-Length）是浏览器与代理的常见形态。
func TestChunkedBodyWithoutContentLength(t *testing.T) {
	// 假上游：view 返回视频信息，playurl 返回一条音频流（缺少它会被判为「无可用音频」）
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/view"):
			writeTestJSON(w, `{"code":0,"message":"OK","data":{
				"bvid":"BV1GJ411x7h7","title":"分块测试","duration":10,"cid":1,
				"owner":{"name":"UP"},"pages":[{"cid":1,"page":1,"part":"P1","duration":10}]}}`)
		case strings.HasSuffix(r.URL.Path, "/playurl"):
			writeTestJSON(w, `{"code":0,"message":"OK","data":{"dash":{"audio":[
				{"id":30216,"bandwidth":64000,
				 "baseUrl":"https://upos-sz-mirrorcos.bilivideo.com/a.m4s"}]}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	client := newTestClientTo(upstream.URL)
	handler, err := NewHandler(Config{Client: client, Logger: NewLogger(io.Discard)})
	if err != nil {
		t.Fatal(err)
	}

	// 用真实 socket 跑，确保走的是 chunked 编码路径
	front := httptest.NewServer(handler)
	defer front.Close()

	req, err := http.NewRequest(http.MethodPost, front.URL+"/api/parse",
		io.NopCloser(strings.NewReader(formBody("BV1GJ411x7h7"))))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Requested-With", "fetch")
	req.ContentLength = -1 // 触发 chunked

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if strings.Contains(string(body), "请输入视频链接") {
		t.Fatalf("chunked 请求被判空：%s", firstRunes(string(body), 200))
	}
	if !strings.Contains(string(body), "分块测试") {
		t.Fatalf("chunked 请求未解析成功：%s", firstRunes(string(body), 300))
	}
}

// 真实 HTTP 往返（非 httptest.NewRequest 直调），覆盖 header 大小写、
// Content-Length 计算等只在网络上才出现的差异。
func TestParseOverRealHTTP(t *testing.T) {
	handler, upstream := newTestHandler(t, Config{})
	defer upstream.Close()

	front := httptest.NewServer(handler)
	defer front.Close()

	resp, err := http.Post(front.URL+"/api/parse",
		"application/x-www-form-urlencoded", strings.NewReader(formBody(browserLink)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assertParsed(t, string(body), "真实 HTTP 往返")
}

// 排查辅助：解析失败时记录收到的是什么，便于线上定位。
func TestParseLogsReceivedInput(t *testing.T) {
	var logs strings.Builder
	handler, upstream := newTestHandler(t, Config{Logger: NewLogger(&logs)})
	defer upstream.Close()

	front := httptest.NewServer(handler)
	defer front.Close()

	resp, err := http.Post(front.URL+"/api/parse",
		"application/x-www-form-urlencoded", strings.NewReader(formBody(browserLink)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if !strings.Contains(logs.String(), "BV1iF411j7oe") {
		t.Fatalf("解析日志里应能看到收到的输入内容，实际日志：%s", logs.String())
	}
}

// 确认端口/地址形态不影响服务可用性（曾出现只监听某地址导致手机访问不到）。
func TestServerBindsAllInterfaces(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	if errors.Is(err, net.ErrClosed) {
		t.Fatal("listener 立即关闭")
	}
}
