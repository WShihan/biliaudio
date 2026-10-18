package web

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// echoHandler 回显请求细节，仅用于排查「服务端收到的 input 为空」这类问题。
//
// 默认关闭，需要 BILIAUDIO_ECHO=1 才启用：它会把请求内容返回给调用方，
// 虽然会隐藏 Cookie 与令牌，但不适合长期暴露在公网。
func echoHandler(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 先把 body 读出来，再塞回去，否则后面的 ParseForm 会读到已关闭的 body
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(raw))
		// 解析表单（与 POST /api/parse 完全相同的调用方式）
		formErr := r.ParseForm()

		headers := map[string]string{}
		for _, key := range []string{
			"Content-Type", "Content-Length", "User-Agent",
			"X-Requested-With", "Referer", "Origin", "Cookie",
		} {
			value := r.Header.Get(key)
			if key == "Cookie" {
				value = maskSecrets(value)
			}
			headers[key] = value
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"method":           r.Method,
			"path":             r.URL.Path,
			"raw_query":        maskSecrets(r.URL.RawQuery),
			"query_input":      r.URL.Query().Get("input"),
			"headers":          headers,
			"raw_body":         maskSecrets(string(raw)),
			"body_bytes":       len(raw),
			"parse_form_error": errText(formErr),
			"form_value_input": r.FormValue("input"),
			"form_value_len":   len(r.FormValue("input")),
			"form_all_keys":    formKeys(r),
			"token_configured": cfg.Token != "",
		})
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// maskSecrets 把令牌与 Cookie 的值打码，避免诊断输出泄露凭据。
func maskSecrets(text string) string {
	if text == "" {
		return ""
	}
	values := url.Values{}
	for _, pair := range strings.Split(text, "&") {
		key, value, found := strings.Cut(pair, "=")
		if !found {
			values.Add(key, "")
			continue
		}
		decoded, err := url.QueryUnescape(value)
		if err != nil {
			decoded = value
		}
		lower := strings.ToLower(key)
		if lower == "token" || strings.Contains(lower, "sessdata") ||
			strings.Contains(lower, "buvid") || lower == "cookie" {
			values.Add(key, mask(decoded))
			continue
		}
		values.Add(key, decoded)
	}
	return values.Encode()
}

func mask(value string) string {
	if len(value) <= 6 {
		return "***"
	}
	return value[:3] + "***" + value[len(value)-3:]
}
