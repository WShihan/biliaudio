package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WShihan/biliaudio/internal/bili"
)

func TestParsePageSelection(t *testing.T) {
	cases := []struct {
		spec string
		want []int
	}{
		{"", nil},
		{"1", []int{1}},
		{"1,3", []int{1, 3}},
		{"2-4", []int{2, 3, 4}},
		{"1, 3-5", []int{1, 3, 4, 5}},
		{"5-3", []int{3, 4, 5}}, // 逆序自动纠正
	}
	for _, tc := range cases {
		got, err := parsePageSelection(tc.spec)
		if err != nil {
			t.Fatalf("parsePageSelection(%q) 报错：%v", tc.spec, err)
		}
		for _, want := range tc.want {
			if !got[want] {
				t.Errorf("parsePageSelection(%q) 缺少 %d（得到 %v）", tc.spec, want, got)
			}
		}
		if tc.want == nil && len(got) != 0 {
			t.Errorf("parsePageSelection(%q) 应为空，得到 %v", tc.spec, got)
		}
		if tc.want != nil && len(got) != len(tc.want) {
			t.Errorf("parsePageSelection(%q) 元素数 %d，期望 %d", tc.spec, len(got), len(tc.want))
		}
	}
}

func TestParsePageSelectionRejectsGarbage(t *testing.T) {
	for _, spec := range []string{"abc", "1-", "-3", "1;2"} {
		if _, err := parsePageSelection(spec); err == nil {
			t.Errorf("parsePageSelection(%q) 应当报错", spec)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"正常标题":               "正常标题",
		"含/斜杠\\与:冒号":         "含_斜杠_与_冒号",
		"多  个   空格":          "多 个 空格",
		"重复___下划线":           "重复_下划线",
		"  首尾空白  ":           "首尾空白",
		"emoji😀会被清掉":         "emoji_会被清掉",
		"【官方 MV】Never Gonna": "【官方 MV】Never Gonna",
		"a?b*c\"d<e>f|g":     "a_b_c_d_e_f_g",
	}
	for input, want := range cases {
		if got := sanitizeName(input); got != want {
			t.Errorf("sanitizeName(%q) = %q，期望 %q", input, got, want)
		}
	}
}

func TestSanitizeNameNeverEmpty(t *testing.T) {
	for _, input := range []string{"", "   ", "///", "😀😀"} {
		got := sanitizeName(input)
		if got == "" {
			t.Errorf("sanitizeName(%q) 不应返回空串", input)
		}
	}
}

func TestSanitizeNameTruncatesByRunes(t *testing.T) {
	long := strings.Repeat("中", 300)
	got := sanitizeName(long)
	if runes := []rune(got); len(runes) > maxNameLength {
		t.Fatalf("超长名称未被截断：%d 个字符", len(runes))
	}
	// 截断不能把多字节字符切坏
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatal("截断产生了非法字符")
	}
}

func TestBuildNameTemplate(t *testing.T) {
	video := &bili.Video{
		Bvid: "BV1GJ411x7h7", Author: "某 UP 主", Title: "总标题",
		Parts: []bili.Part{{Index: 1, Page: 1}, {Index: 2, Page: 2}},
	}
	part := bili.Part{Index: 2, Page: 2, Name: "P2 · 第二话", Title: "第二话"}

	data := partNameData(video, part)
	cases := map[string]string{
		"{title}":            "第二话",
		"{bvid}":             "BV1GJ411x7h7",
		"{author}":           "某 UP 主",
		"{page}":             "P2",
		"{index}":            "2",
		"{author} - {title}": "某 UP 主 - 第二话",
		"{bvid}_{index}":     "BV1GJ411x7h7_2",
	}
	for template, want := range cases {
		if got := buildName(template, data); got != want {
			t.Errorf("buildName(%q) = %q，期望 %q", template, got, want)
		}
	}
}

// 单 P 视频的 {page} 应为空，默认模板 " - {title}" 才不会出现多余前缀。
func TestPartNameDataSinglePage(t *testing.T) {
	video := &bili.Video{Bvid: "BV1GJ411x7h7", Author: "UP", Title: "整段音频", Parts: []bili.Part{{Index: 1, Page: 1}}}
	part := bili.Part{Index: 1, Page: 1, Name: "完整音频", Title: "整段音频"}

	got := buildName(defaultTemplate, partNameData(video, part))
	if got != "整段音频" {
		t.Fatalf("单 P 默认文件名 = %q，期望 %q", got, "整段音频")
	}
}

func TestOutputPathAppendsExtension(t *testing.T) {
	if got := outputPath("/tmp", "歌曲"); got != filepath.Join("/tmp", "歌曲.m4a") {
		t.Errorf("outputPath = %q", got)
	}
	if got := outputPath("/tmp", "歌曲.m4a"); got != filepath.Join("/tmp", "歌曲.m4a") {
		t.Errorf("已有扩展名不应重复追加：%q", got)
	}
}

// 端到端：用假的 B 站接口与音频服务器跑通「解析 → 选分 P → 下载」全流程。
func TestRunEndToEnd(t *testing.T) {
	audio := bytes.Repeat([]byte("audio-data-"), 4096) // 约 45 KB

	audioServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != bili.Referer {
			t.Errorf("音频请求缺少 Referer")
		}
		w.Header().Set("Content-Length", itoa(len(audio)))
		_, _ = w.Write(audio)
	}))
	defer audioServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/view"):
			if got := r.URL.Query().Get("bvid"); got != "BV1GJ411x7h7" {
				t.Errorf("view 请求的 bvid 不对：%q", got)
			}
			_, _ = w.Write([]byte(`{"code":0,"message":"OK","data":{
				"bvid":"BV1GJ411x7h7","title":"测试合集","duration":120,
				"pic":"http://i0.hdslb.com/cover.jpg","pubdate":1577836800,
				"desc":"简介","owner":{"name":"测试UP"},
				"stat":{"view":12345,"like":678,"coin":9,"favorite":1,"share":2,"danmaku":3},
				"pages":[{"cid":111,"page":1,"part":"第一话","duration":60},
				         {"cid":222,"page":2,"part":"第二话","duration":60}]}}`))
		case strings.Contains(r.URL.Path, "/playurl"):
			cid := r.URL.Query().Get("cid")
			_, _ = w.Write([]byte(`{"code":0,"message":"OK","data":{"dash":{"audio":[
				{"id":30216,"bandwidth":64000,"baseUrl":"` + audioServer.URL + `/low.m4s"},
				{"id":30280,"bandwidth":192000,"baseUrl":"` + audioServer.URL + `/` + cid + `.m4s"}]}}}`))
		default:
			t.Errorf("未预期的接口请求：%s", r.URL.Path)
		}
	}))
	defer apiServer.Close()

	client := bili.NewClient("")
	client.HTTP = &http.Client{Transport: apiTransport(apiServer.URL)}

	outDir := t.TempDir()
	selected, err := parsePageSelection("2")
	if err != nil {
		t.Fatal(err)
	}

	console := &Console{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	cfg := config{outDir: outDir, template: defaultTemplate, quiet: true}
	summary := &runSummary{}

	video, err := client.FetchVideo(context.Background(), "BV1GJ411x7h7")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if video.Title != "测试合集" || video.Author != "测试UP" || len(video.Parts) != 2 {
		t.Fatalf("视频信息解析不对：%+v", video)
	}
	if video.Stats.View != 12345 || bili.FormatCount(video.Stats.View) != "1.2万" {
		t.Errorf("统计数据不对：%+v", video.Stats)
	}

	printVideoInfo(console, video)
	for _, part := range filterParts(video, selected) {
		downloadPart(context.Background(), console, client, cfg, video, part, summary)
	}

	if summary.downloaded != 1 || summary.failed != 0 {
		t.Fatalf("期望下载 1 个且无失败，实际 %+v", summary)
	}
	if summary.bytes != int64(len(audio)) {
		t.Errorf("下载字节数 %d，期望 %d", summary.bytes, len(audio))
	}

	// 只选了 P2，因此只应产出第二话
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("期望产出 1 个文件，实际 %d 个", len(entries))
	}
	if name := entries[0].Name(); !strings.Contains(name, "第二话") {
		t.Errorf("文件名应包含分 P 名，实际 %q", name)
	}

	downloaded, err := os.ReadFile(filepath.Join(outDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(downloaded, audio) {
		t.Error("下载内容与源不一致")
	}

	// 重复运行时应当跳过
	summary2 := &runSummary{}
	for _, part := range filterParts(video, selected) {
		downloadPart(context.Background(), console, client, cfg, video, part, summary2)
	}
	if summary2.skipped != 1 || summary2.downloaded != 0 {
		t.Fatalf("重复运行应跳过，实际 %+v", summary2)
	}
}

// 选中的分 P 不存在时不应崩，只是没有可下载项。
func TestFilterPartsEmptyWhenNoMatch(t *testing.T) {
	video := &bili.Video{Parts: []bili.Part{{Index: 1, Page: 1}, {Index: 2, Page: 2}}}
	if got := filterParts(video, map[int]bool{9: true}); len(got) != 0 {
		t.Fatalf("期望空结果，实际 %v", got)
	}
	if got := filterParts(video, nil); len(got) != 2 {
		t.Fatalf("未指定时应返回全部分 P，实际 %v", got)
	}
}

// apiTransport 把所有 B 站接口域名指向假服务器，保证测试不触碰真实网络。
func apiTransport(baseURL string) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cloned := req.Clone(req.Context())
		cloned.URL.Scheme = "http"
		cloned.URL.Host = strings.TrimPrefix(baseURL, "http://")
		return http.DefaultTransport.RoundTrip(cloned)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		512:                    "512 B",
		1024:                   "1.0 KB",
		1536:                   "1.5 KB",
		5 * 1024 * 1024:        "5.0 MB",
		3 * 1024 * 1024 * 1024: "3.0 GB",
	}
	for input, want := range cases {
		if got := humanBytes(input); got != want {
			t.Errorf("humanBytes(%d) = %q，期望 %q", input, got, want)
		}
	}
}
