// Package bili 封装 B 站 web 接口：视频信息、分 P 列表、DASH 音频直链。
//
// 只依赖标准库。所有请求都带上 Referer/Origin/UA，否则 B 站会拒绝媒体直链。
package bili

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	ViewAPI   = "https://api.bilibili.com/x/web-interface/view"
	PlayAPI   = "https://api.bilibili.com/x/player/playurl"
	Referer   = "https://www.bilibili.com/"
	Origin    = "https://www.bilibili.com"
	UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"

	// fnval: 16=DASH，4048=Hi-Res + 杜比全景声；只要音频时也开满，拿到的
	// dash.audio 里自然会包含最高档。
	fnval = 16 | 4048
)

// Error 表示 B 站接口返回的业务错误，错误码与原始响应都保留下来便于排查。
type Error struct {
	Code    int
	Message string
	Status  int
	Body    string
}

func (e *Error) Error() string {
	switch {
	case e.Code != 0:
		return fmt.Sprintf("B 站接口错误 %d：%s", e.Code, e.Message)
	case e.Status != 0:
		return fmt.Sprintf("B 站返回 HTTP %d%s", e.Status, e.BodyHint())
	default:
		return "B 站接口返回了非预期的内容"
	}
}

// BodyHint 返回响应片段，用于在出错时提示是否被风控拦截。
func (e *Error) BodyHint() string {
	snippet := strings.TrimSpace(e.Body)
	if len(snippet) > 200 {
		snippet = snippet[:200] + "..."
	}
	if snippet == "" {
		return ""
	}
	return "：" + snippet
}

// Client 访问 B 站接口。零值不可用，请用 NewClient。
type Client struct {
	HTTP   *http.Client
	Cookie string
	// Transport 可选，用于替换底层传输层（测试注入、或走代理出网）
	Transport http.RoundTripper
}

// NewClient 创建客户端。cookie 可为空（匿名访问），也支持只填 SESSDATA 的值。
func NewClient(cookie string) *Client {
	cookie = strings.TrimSpace(cookie)
	if cookie != "" && !strings.Contains(cookie, "=") {
		cookie = "SESSDATA=" + cookie
	}
	return &Client{
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
			// 媒体直链会 302 到具体 CDN，跟随跳转由调用方决定（这里只用于接口）
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		Cookie: cookie,
	}
}

// WithTransport 返回一个使用指定传输层的副本。
func (c *Client) WithTransport(transport http.RoundTripper) *Client {
	clone := *c
	clone.Transport = transport
	clone.HTTP = &http.Client{
		Timeout:       c.HTTP.Timeout,
		Transport:     transport,
		CheckRedirect: c.HTTP.CheckRedirect,
	}
	return &clone
}

type apiEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// ViewData 是 view 接口的原始返回，字段很多，这里只声明用得到的部分。
type ViewData struct {
	Bvid     string `json:"bvid"`
	Aid      int64  `json:"aid"`
	Cid      int64  `json:"cid"`
	Title    string `json:"title"`
	Pic      string `json:"pic"`
	Desc     string `json:"desc"`
	Duration int    `json:"duration"`
	Pubdate  int64  `json:"pubdate"`
	Owner    struct {
		Name string `json:"name"`
	} `json:"owner"`
	Stat struct {
		View    int64 `json:"view"`
		Like    int64 `json:"like"`
		Coin    int64 `json:"coin"`
		Fav     int64 `json:"favorite"`
		Share   int64 `json:"share"`
		Danmaku int64 `json:"danmaku"`
	} `json:"stat"`
	Pages []PageData `json:"pages"`
}

// PageData 是单个分 P。
type PageData struct {
	Cid      int64  `json:"cid"`
	Page     int    `json:"page"`
	Part     string `json:"part"`
	Duration int    `json:"duration"`
}

type playData struct {
	Dash *struct {
		Audio []struct {
			ID        int      `json:"id"`
			Bandwidth int64    `json:"bandwidth"`
			MimeType  string   `json:"mimeType"`
			Codecs    string   `json:"codecs"`
			BaseURL   string   `json:"baseUrl"`
			BackupURL []string `json:"backupUrl"`
		} `json:"audio"`
	} `json:"dash"`
}

// Video 是一次解析的产物，已整理成下载所需的最小信息。
type Video struct {
	Bvid     string
	Title    string
	Author   string
	Cover    string
	Desc     string
	Duration time.Duration
	Pubdate  time.Time
	Stats    Stats
	Parts    []Part
}

// Stats 是视频统计数据。
type Stats struct {
	View    int64
	Like    int64
	Coin    int64
	Fav     int64
	Share   int64
	Danmaku int64
}

// Part 是一个可下载的音频分段（单 P 视频即为整体）。
type Part struct {
	Index    int    // 从 1 开始的分 P 序号
	Page     int    // 接口返回的 page
	Name     string // 展示名，如 "P1 · 某标题" 或 "完整音频"
	Title    string // 用于命名的标题，如 "某视频" 或 "某标题"
	Cid      int64
	Duration time.Duration
}

// FetchVideo 按 BV/av 号获取视频信息，并展开分 P 列表。
func (c *Client) FetchVideo(ctx context.Context, id string) (*Video, error) {
	params := map[string]string{}
	if bv, ok := Bvid(id); ok {
		params["bvid"] = bv
	} else if av, ok := Avid(id); ok {
		params["aid"] = av
	} else {
		return nil, fmt.Errorf("无法识别的视频 ID：%q", id)
	}

	var data ViewData
	if err := c.getJSON(ctx, ViewAPI, params, &data); err != nil {
		return nil, err
	}

	video := &Video{
		Bvid:     BvidNormalize(data.Bvid),
		Title:    data.Title,
		Author:   data.Owner.Name,
		Cover:    strings.Replace(data.Pic, "http://", "https://", 1),
		Desc:     data.Desc,
		Duration: time.Duration(data.Duration) * time.Second,
		Stats: Stats{
			View:    data.Stat.View,
			Like:    data.Stat.Like,
			Coin:    data.Stat.Coin,
			Fav:     data.Stat.Fav,
			Share:   data.Stat.Share,
			Danmaku: data.Stat.Danmaku,
		},
	}
	if data.Pubdate > 0 {
		video.Pubdate = time.Unix(data.Pubdate, 0)
	}
	if video.Author == "" {
		video.Author = "未知 UP 主"
	}

	video.Parts = buildParts(data, video.Title)
	if len(video.Parts) == 0 {
		return nil, fmt.Errorf("%s：没有可用的音频分段", id)
	}
	return video, nil
}

// buildParts 把接口的 pages 展开成下载分段；多 P 用分 P 名，单 P 用整体标题。
func buildParts(data ViewData, title string) []Part {
	parts := make([]Part, 0, len(data.Pages))
	if len(data.Pages) > 1 {
		for i, page := range data.Pages {
			if page.Cid == 0 {
				continue
			}
			no := page.Page
			if no == 0 {
				no = i + 1
			}
			name := fmt.Sprintf("P%d", no)
			partTitle := title
			if p := strings.TrimSpace(page.Part); p != "" {
				name += " · " + p
				partTitle = p
			}
			parts = append(parts, Part{
				Index:    len(parts) + 1,
				Page:     no,
				Name:     name,
				Title:    partTitle,
				Cid:      page.Cid,
				Duration: time.Duration(page.Duration) * time.Second,
			})
		}
		return parts
	}

	cid := data.Cid
	if cid == 0 && len(data.Pages) == 1 {
		cid = data.Pages[0].Cid
	}
	if cid == 0 {
		return nil
	}
	return []Part{{
		Index:    1,
		Page:     1,
		Name:     "完整音频",
		Title:    title,
		Cid:      cid,
		Duration: time.Duration(data.Duration) * time.Second,
	}}
}

// AudioURL 取指定分 P 码率最高的音频直链。
func (c *Client) AudioURL(ctx context.Context, bvid string, cid int64) (string, error) {
	var data playData
	err := c.getJSON(ctx, PlayAPI, map[string]string{
		"bvid":  bvid,
		"cid":   fmt.Sprint(cid),
		"fnval": fmt.Sprint(fnval),
		"fnver": "0",
		"fourk": "1",
	}, &data)
	if err != nil {
		return "", err
	}
	if data.Dash == nil || len(data.Dash.Audio) == 0 {
		return "", errors.New("该视频没有可用的音频流")
	}

	best := data.Dash.Audio[0]
	for _, audio := range data.Dash.Audio[1:] {
		if audio.Bandwidth > best.Bandwidth {
			best = audio
		}
	}
	if best.BaseURL != "" {
		return best.BaseURL, nil
	}
	if len(best.BackupURL) > 0 {
		return best.BackupURL[0], nil
	}
	return "", errors.New("音频直链为空")
}

// getJSON 发一次接口请求并校验返回码。
func (c *Client) getJSON(ctx context.Context, endpoint string, params map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	query := req.URL.Query()
	for key, value := range params {
		query.Set(key, value)
	}
	req.URL.RawQuery = query.Encode()
	req.Header = c.Headers()

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return describeTransportError(err, endpoint)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<22))
	if err != nil {
		return fmt.Errorf("读取接口响应失败：%w", err)
	}

	var envelope apiEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return &Error{Status: resp.StatusCode, Body: string(body)}
	}
	if envelope.Code != 0 {
		return &Error{Code: envelope.Code, Message: envelope.Message, Status: resp.StatusCode}
	}
	if resp.StatusCode != http.StatusOK {
		return &Error{Status: resp.StatusCode, Body: string(body)}
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("解析接口数据失败：%w", err)
	}
	return nil
}

// Headers 返回访问 B 站接口与媒体所需的请求头。
func (c *Client) Headers() http.Header {
	header := http.Header{}
	header.Set("Referer", Referer)
	header.Set("Origin", Origin)
	header.Set("User-Agent", UserAgent)
	header.Set("Accept", "*/*")
	header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	if c.Cookie != "" {
		header.Set("Cookie", c.Cookie)
	}
	return header
}

// describeTransportError 把网络层错误说得更具体，超时与连不上要能区分开。
func describeTransportError(err error, endpoint string) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return fmt.Errorf("访问 %s 超时：%w", endpoint, err)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("已取消：%w", err)
	default:
		return fmt.Errorf("无法访问 %s：%w", endpoint, err)
	}
}
