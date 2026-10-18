package input

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// fakeRedirect 返回一个只为短链解析服务的客户端。
func fakeRedirect(t *testing.T, location string, status int) *Extractor {
	t.Helper()
	return &Extractor{
		HTTP: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodHead {
					t.Errorf("短链解析应该用 HEAD 请求，实际是 %s", req.Method)
				}
				header := http.Header{}
				if location != "" {
					header.Set("Location", location)
				}
				return &http.Response{
					StatusCode: status,
					Header:     header,
					Body:       http.NoBody,
					Request:    req,
				}, nil
			}),
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestExtractWithoutNetwork(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{"裸 BV 号", "BV1GJ411x7h7", []string{"BV1GJ411x7h7"}},
		{"av 号", "av80433022", []string{"av80433022"}},
		{"分享文案", "【标题】 https://www.bilibili.com/video/BV1GJ411x7h7?share=1 复制打开", []string{"BV1GJ411x7h7"}},
		{"多个去重", "BV1GJ411x7h7 和 BV1xx411c7mD 以及 BV1GJ411x7h7", []string{"BV1GJ411x7h7", "BV1xx411c7mD"}},
		{"多个链接", "https://www.bilibili.com/video/BV1xx411c7mD\nhttps://www.bilibili.com/video/av80433022", []string{"BV1xx411c7mD", "av80433022"}},
		{"小写前缀", "bv1GJ411x7h7", []string{"BV1GJ411x7h7"}},
		{"空文本", "   ", nil},
		{"无有效内容", "今天天气不错", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 用 nil 客户端：本组用例不应触发任何网络请求
			got, err := (&Extractor{}).Extract(context.Background(), tc.input)
			if err != nil {
				t.Fatalf("不应报错：%v", err)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("Extract(%q) = %v，期望 %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestExtractResolvesShortLink(t *testing.T) {
	extractor := fakeRedirect(t, "https://www.bilibili.com/video/BV1GJ411x7h7?spm=1", http.StatusFound)

	got, err := extractor.Extract(context.Background(), "分享 https://b23.tv/abc123XY 给你")
	if err != nil {
		t.Fatalf("短链解析不应报错：%v", err)
	}
	if len(got) != 1 || got[0] != "BV1GJ411x7h7" {
		t.Fatalf("短链解析结果 = %v，期望 [BV1GJ411x7h7]", got)
	}
}

func TestExtractShortLinkDeduplicates(t *testing.T) {
	var calls int
	extractor := &Extractor{HTTP: &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			header := http.Header{}
			header.Set("Location", "https://www.bilibili.com/video/BV1GJ411x7h7")
			return &http.Response{StatusCode: http.StatusFound, Header: header, Body: http.NoBody, Request: req}, nil
		}),
		// 与生产代码保持一致：只取第一跳的 Location，不真的跟着跳
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}

	got, err := extractor.Extract(context.Background(), "https://b23.tv/aaa 与 https://b23.tv/aaa")
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if calls != 1 {
		t.Errorf("同一短链应只请求一次，实际 %d 次", calls)
	}
	if len(got) != 1 {
		t.Fatalf("结果应去重：%v", got)
	}
}

// 短链解析失败时，已识别到的其它 ID 仍应保留，错误只作为警告上报。
func TestExtractKeepsIDsWhenShortLinkFails(t *testing.T) {
	extractor := fakeRedirect(t, "", http.StatusNotFound)

	got, err := extractor.Extract(context.Background(), "https://b23.tv/broken BV1GJ411x7h7")
	if err == nil {
		t.Error("短链失败时应返回错误以供提示")
	}
	if len(got) != 1 || got[0] != "BV1GJ411x7h7" {
		t.Fatalf("裸 BV 号应被保留，实际 %v", got)
	}
}

func TestExtractShortLinkWithoutRedirect(t *testing.T) {
	// 短链直接 200 且没有 Location 时，视为它本身就是内容地址
	extractor := fakeRedirect(t, "", http.StatusOK)
	got, err := extractor.Extract(context.Background(), "https://b23.tv/abc123XY")
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if len(got) != 0 {
		t.Fatalf("无 Location 时不应产生 ID，实际 %v", got)
	}
}
