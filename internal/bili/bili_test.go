package bili

import "testing"

func TestBvidExtraction(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{"裸 BV 号", "BV1GJ411x7h7", "BV1GJ411x7h7", true},
		{"小写前缀会被修正", "bv1GJ411x7h7", "BV1GJ411x7h7", true},
		{"夹在文本里", "看这个 BV1GJ411x7h7 很有意思", "BV1GJ411x7h7", true},
		{"完整链接", "https://www.bilibili.com/video/BV1GJ411x7h7?spm=1", "BV1GJ411x7h7", true},
		{"没有 BV 号", "随便一段文字", "", false},
		{"长度不足", "BV1GJ411x7h", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Bvid(tc.input)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("Bvid(%q) = (%q, %v)，期望 (%q, %v)", tc.input, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// BV 号本体大小写敏感，绝不能整体大写，否则会指向另一个视频。
func TestBvidCaseIsPreserved(t *testing.T) {
	got, ok := Bvid("BV1GJ411x7h7")
	if !ok || got != "BV1GJ411x7h7" {
		t.Fatalf("大小写被改动了：%q", got)
	}
	if lower, _ := Bvid("BV1gj411x7h7"); lower == got {
		t.Fatal("两个不同的 BV 号被当成同一个")
	}
}

func TestAvidExtraction(t *testing.T) {
	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{"av80433022", "80433022", true},
		{"AV80433022", "80433022", true},
		{"看 av80433022 这个", "80433022", true},
		{"https://www.bilibili.com/video/av80433022", "80433022", true},
		{"have 123", "", false}, // av 前面是字母，不算
		{"nav123", "", false},   // 同上
	}
	for _, tc := range cases {
		got, ok := Avid(tc.input)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("Avid(%q) = (%q, %v)，期望 (%q, %v)", tc.input, got, ok, tc.want, tc.ok)
		}
	}
}

func TestFormatCount(t *testing.T) {
	cases := map[int64]string{
		0:           "0",
		999:         "999",
		10_000:      "1万",
		12_345:      "1.2万",
		1_500_000:   "150万",
		150_000_000: "1.5亿",
	}
	for input, want := range cases {
		if got := FormatCount(input); got != want {
			t.Errorf("FormatCount(%d) = %q，期望 %q", input, got, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[int]string{
		0:    "0:00",
		59:   "0:59",
		60:   "1:00",
		213:  "3:33",
		3600: "1:00:00",
		3661: "1:01:01",
	}
	for input, want := range cases {
		if got := FormatDuration(input); got != want {
			t.Errorf("FormatDuration(%d) = %q，期望 %q", input, got, want)
		}
	}
}

func TestBuildParts(t *testing.T) {
	t.Run("单 P 用整体标题", func(t *testing.T) {
		data := ViewData{Bvid: "BV1GJ411x7h7", Cid: 100, Title: "某视频", Duration: 213}
		parts := buildParts(data, data.Title)
		if len(parts) != 1 {
			t.Fatalf("期望 1 个分 P，实际 %d", len(parts))
		}
		if parts[0].Name != "完整音频" || parts[0].Title != "某视频" || parts[0].Cid != 100 {
			t.Fatalf("单 P 结果不对：%+v", parts[0])
		}
	})

	t.Run("多 P 用分 P 名并跳过缺失 cid", func(t *testing.T) {
		data := ViewData{
			Title: "合集",
			Pages: []PageData{
				{Cid: 1, Page: 1, Part: "第一话", Duration: 60},
				{Cid: 0, Page: 2, Part: "坏数据"},
				{Cid: 3, Page: 3, Duration: 30},
			},
		}
		parts := buildParts(data, data.Title)
		if len(parts) != 2 {
			t.Fatalf("期望 2 个分 P（跳过 cid=0），实际 %d", len(parts))
		}
		if parts[0].Name != "P1 · 第一话" || parts[0].Title != "第一话" {
			t.Errorf("第一个分 P 不对：%+v", parts[0])
		}
		if parts[1].Name != "P3" || parts[1].Title != "合集" {
			t.Errorf("第二个分 P 不对：%+v", parts[1])
		}
		if parts[0].Index != 1 || parts[1].Index != 2 {
			t.Errorf("Index 应为连续序号：%d, %d", parts[0].Index, parts[1].Index)
		}
	})

	t.Run("无 cid 时返回空", func(t *testing.T) {
		if parts := buildParts(ViewData{Title: "x"}, "x"); len(parts) != 0 {
			t.Fatalf("期望空列表，实际 %+v", parts)
		}
	})
}

func TestErrorMessages(t *testing.T) {
	business := &Error{Code: -404, Message: "啥都木有"}
	if got := business.Error(); got != "B 站接口错误 -404：啥都木有" {
		t.Errorf("业务错误提示不对：%q", got)
	}

	blocked := &Error{Status: 412, Body: "<html>412</html>"}
	if got := blocked.Error(); got != "B 站返回 HTTP 412：<html>412</html>" {
		t.Errorf("状态码错误提示不对：%q", got)
	}
}
