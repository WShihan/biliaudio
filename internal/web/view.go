package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strings"

	bilibili "github.com/WShihan/biliaudio/internal/bili"
	"github.com/WShihan/biliaudio/internal/input"
)

const tokenCookie = "biliaudio_token"

// parseTemplates 解析内嵌模板。
func parseTemplates() (*template.Template, error) {
	tmpl, err := template.New("index.html").
		Funcs(template.FuncMap{"add": func(a, b int) int { return a + b }}).
		ParseFS(Assets(), "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("解析模板失败：%w", err)
	}
	return tmpl, nil
}

// collect 解析输入文本，产出模板/JSON 需要的视图数据。
func collect(r *http.Request, client *bilibili.Client, cfg Config, text string) *ViewData {
	ctx := r.Context()
	data := NewViewData()
	data.Input = text

	ids, err := input.NewExtractor().Extract(ctx, text)
	if err != nil {
		data.Failures = append(data.Failures, err.Error())
	}
	if len(ids) == 0 {
		data.Error = "未识别到有效的视频链接或 BV/av 号"
		return data
	}
	if len(ids) > MaxVideos {
		data.Failures = append(data.Failures,
			fmt.Sprintf("单次最多处理 %d 个视频，多余的已忽略", MaxVideos))
		ids = ids[:MaxVideos]
	}

	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		view, err := buildView(ctx, client, id)
		if err != nil {
			cfg.Logger.Warn("解析 %s 失败: %v", id, err)
			data.Failures = append(data.Failures, fmt.Sprintf("%s：%v", id, err))
			continue
		}
		data.Videos = append(data.Videos, *view)
	}

	if len(data.Videos) == 0 && len(data.Failures) > 0 {
		data.Error = data.Failures[0]
	}
	data.Tracks = collectTracks(data.Videos)

	encoded, err := json.Marshal(data.Tracks)
	if err != nil {
		encoded = []byte("[]")
	}
	data.TracksJSON = template.JS(encoded)
	return data
}

// buildView 把一个视频整理成展示用结构，并解析每个分 P 的音频直链。
func buildView(ctx context.Context, client *bilibili.Client, id string) (*ViewVideo, error) {
	video, err := client.FetchVideo(ctx, id)
	if err != nil {
		return nil, err
	}

	view := &ViewVideo{
		Bvid:        video.Bvid,
		Title:       video.Title,
		Author:      video.Author,
		Cover:       video.Cover,
		Description: truncate(video.Desc, 180),
		Duration:    bilibili.FormatDuration(int(video.Duration.Seconds())),
		Views:       bilibili.FormatCount(video.Stats.View),
		Likes:       bilibili.FormatCount(video.Stats.Like),
		Coins:       bilibili.FormatCount(video.Stats.Coin),
		Favorites:   bilibili.FormatCount(video.Stats.Fav),
		Shares:      bilibili.FormatCount(video.Stats.Share),
		Danmaku:     bilibili.FormatCount(video.Stats.Danmaku),
	}
	if !video.Pubdate.IsZero() {
		view.Pubdate = video.Pubdate.Local().Format("2006-01-02")
	}

	multi := len(video.Parts) > 1
	for _, part := range video.Parts {
		streamURL, err := client.AudioURL(ctx, video.Bvid, part.Cid)
		if err != nil {
			// 单个分 P 失败不应该影响其它分 P
			continue
		}
		if err := bilibili.CheckMediaURL(streamURL); err != nil {
			continue
		}
		trackName := video.Title
		if multi {
			trackName = part.Name + " - " + video.Title
		}
		view.Parts = append(view.Parts, ViewPart{
			Name:      part.Name,
			TrackName: trackName,
			Duration:  bilibili.FormatDuration(int(part.Duration.Seconds())),
			Filename:  part.Title,
			ProxyURL:  proxyURL(streamURL, part.Title),
		})
	}

	if len(view.Parts) == 0 {
		return nil, fmt.Errorf("该视频没有可用的音频流")
	}
	return view, nil
}

// proxyURL 生成站内代理地址：直链只以 base64url 形式出现，页面里不暴露上游签名。
//
// filename 用 PathEscape 而不是 QueryEscape：后者把空格编成 "+"，而 "+" 在
// HTML 属性里会被转义成 &#43;，浏览器解码后再交给服务端，Query 解析又会把 "+"
// 还原成空格，文件名会被从第一个空格处截断。
func proxyURL(streamURL, filename string) string {
	return "/api/proxy/" + bilibili.EncodeToken(streamURL) +
		"?filename=" + url.PathEscape(filename)
}

// collectTracks 汇总底部播放器的播放列表。
func collectTracks(videos []ViewVideo) []Track {
	tracks := make([]Track, 0, 4)
	for _, video := range videos {
		for _, part := range video.Parts {
			tracks = append(tracks, Track{
				Name:   part.TrackName,
				Artist: video.Author,
				URL:    part.ProxyURL,
				Cover:  video.Cover,
			})
		}
	}
	return tracks
}

// clientIP 取客户端地址，优先信任反向代理注入的 X-Forwarded-For。
func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if first, _, found := strings.Cut(forwarded, ","); found {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(forwarded)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// secureEqual 定时安全比较，避免令牌被逐字符试探。
func secureEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func trimSpace(s string) string { return strings.TrimSpace(s) }

// truncate 按字数（而非字节）截断长文本。
func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit])) + "..."
}
