// Package input 从用户给的文本里挑出视频 ID。
//
// 支持的输入形态：BV 号、av 号、b23.tv 短链、bilibili.com 完整链接，以及把上述
// 内容夹在任意文本里（比如整个分享文案）。短链需要发 HEAD 请求看 Location。
package input

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/WShihan/biliaudio/internal/bili"
)

const (
	maxShortLinks = 8
	// 短链跳转解析超时：这只是锦上添花，失败就当没识别到
	redirectTimeout = 8 * time.Second
)

var (
	shortPattern   = regexp.MustCompile(`(?i)https?://b23\.tv/([0-9A-Za-z]+)`)
	biliURLPattern = regexp.MustCompile(
		`(?i)https?://(?:[\w-]+\.)?bilibili\.com/video/[^\s"'<>]+`)
	// 裸 BV 号：前后不能紧跟字母数字，避免从长串里切出不完整 ID
	bareBvPattern = regexp.MustCompile(`(?i)(?:^|[^0-9A-Za-z])(BV[0-9A-Za-z]{10})(?:[^0-9A-Za-z]|$)`)
	bareAvPattern = regexp.MustCompile(`(?i)(?:^|[^0-9A-Za-z])av(\d+)(?:[^0-9A-Za-z]|$)`)
)

// Extractor 解析短链。零值可用，使用默认 HTTP 客户端。
type Extractor struct {
	HTTP *http.Client
}

// NewExtractor 创建解析器。
func NewExtractor() *Extractor {
	return &Extractor{
		HTTP: &http.Client{
			Timeout: redirectTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// 只要第一跳的 Location，不真的把页面拉下来
				return http.ErrUseLastResponse
			},
		},
	}
}

// Extract 按出现顺序返回去重后的视频 ID 列表，失败时返回已解析到的部分与错误。
func (e *Extractor) Extract(ctx context.Context, text string) ([]string, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}

	var ids []string
	ids = append(ids, findBareIDs(text)...)
	for _, match := range biliURLPattern.FindAllString(text, -1) {
		ids = append(ids, findBareIDs(match)...)
	}

	// 短链去重后再解析，避免同一链接重复请求
	seenShort := map[string]bool{}
	var shortErr error
	for _, match := range shortPattern.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 || seenShort[match[1]] {
			continue
		}
		seenShort[match[1]] = true
		if len(seenShort) > maxShortLinks {
			break
		}
		target, err := e.resolve(ctx, "https://b23.tv/"+match[1])
		if err != nil {
			shortErr = err
			continue
		}
		ids = append(ids, findBareIDs(target)...)
	}

	return dedupe(ids), shortErr
}

// findBareIDs 从文本里直接抠出 BV / av 号，不发网络请求。
func findBareIDs(text string) []string {
	var ids []string
	for _, match := range bareBvPattern.FindAllStringSubmatch(text, -1) {
		ids = append(ids, bili.BvidNormalize(match[1]))
	}
	for _, match := range bareAvPattern.FindAllStringSubmatch(text, -1) {
		ids = append(ids, "av"+match[1])
	}
	return ids
}

func (e *Extractor) resolve(ctx context.Context, short string) (string, error) {
	client := e.HTTP
	if client == nil {
		client = NewExtractor().HTTP
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, short, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", bili.UserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("解析短链 %s 失败：%w", short, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("解析短链 %s 失败：HTTP %d", short, resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if location == "" {
		// 没有跳转，说明短链直接指向内容
		return short, nil
	}
	if !strings.HasPrefix(location, "http") {
		return "", errors.New("短链跳转地址不可识别：" + location)
	}
	return location, nil
}

// dedupe 保持首次出现的顺序去重。
func dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
