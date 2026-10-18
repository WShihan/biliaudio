package download

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/WShihan/biliaudio/internal/bili"
)

// newTestFile 生成 n 字节的可校验内容。
func newTestFile(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte('a' + i%26)
	}
	return data
}

func TestDownloadFullFile(t *testing.T) {
	payload := newTestFile(300 * 1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Referer"); got != bili.Referer {
			t.Errorf("缺少 Referer 头：%q", got)
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "audio.m4a")

	var lastWritten int64
	result, err := Download(context.Background(), bili.NewClient(""), server.URL, path, func(written, total int64) {
		lastWritten = written
	})
	if err != nil {
		t.Fatalf("下载失败：%v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取产物失败：%v", err)
	}
	if string(data) != string(payload) {
		t.Fatal("文件内容与源不一致")
	}
	if result.Bytes != int64(len(payload)) {
		t.Errorf("Bytes = %d，期望 %d", result.Bytes, len(payload))
	}
	if lastWritten != int64(len(payload)) {
		t.Errorf("进度回调最后停在 %d，期望 %d", lastWritten, len(payload))
	}
	if _, err := os.Stat(path + ".part"); !os.IsNotExist(err) {
		t.Error("下载完成后不应残留 .part 文件")
	}
}

func TestDownloadCreatesMissingDirectory(t *testing.T) {
	payload := newTestFile(1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "深/一层/audio.m4a")
	if _, err := Download(context.Background(), bili.NewClient(""), server.URL, path, nil); err != nil {
		t.Fatalf("下载失败：%v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("文件未生成：%v", err)
	}
}

func TestDownloadResumesFromPartialFile(t *testing.T) {
	payload := newTestFile(200 * 1024)
	const already = 64 * 1024

	var sawRange atomic.Value
	var requestedStart int64 = -1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		sawRange.Store(rangeHeader)
		if rangeHeader == "" {
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			_, _ = w.Write(payload)
			return
		}
		if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-", &requestedStart); err != nil {
			t.Errorf("Range 头格式异常：%q", rangeHeader)
		}
		rest := payload[requestedStart:]
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", requestedStart, len(payload)-1, len(payload)))
		w.Header().Set("Content-Length", fmt.Sprint(len(rest)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(rest)
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "audio.m4a")
	if err := os.WriteFile(path+".part", payload[:already], 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Download(context.Background(), bili.NewClient(""), server.URL, path, nil)
	if err != nil {
		t.Fatalf("续传失败：%v", err)
	}
	if !result.Resumed {
		t.Error("Resumed 应为 true")
	}
	if requestedStart != already {
		t.Errorf("续传起点 %d，期望 %d", requestedStart, already)
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(payload) {
		t.Fatal("续传后内容不一致")
	}
}

// 服务端忽略 Range（返回 200）时必须重新完整下载，不能把新数据追加到旧数据后面。
func TestDownloadRestartsWhenServerIgnoresRange(t *testing.T) {
	payload := newTestFile(50 * 1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "audio.m4a")
	if err := os.WriteFile(path+".part", []byte("旧的脏数据"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Download(context.Background(), bili.NewClient(""), server.URL, path, nil); err != nil {
		t.Fatalf("下载失败：%v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(payload) {
		t.Fatalf("内容被污染，长度 %d，期望 %d", len(data), len(payload))
	}
}

// 服务端中途断开时应重试，并在重试请求里带上 Range 续传。
func TestDownloadRetriesAfterInterruptedStream(t *testing.T) {
	payload := newTestFile(120 * 1024)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := requests.Add(1)
		if attempt == 1 {
			// 声明完整长度却只发一半就断开
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			_, _ = w.Write(payload[:len(payload)/2])
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			panic(http.ErrAbortHandler)
		}
		start := int64(0)
		if rh := r.Header.Get("Range"); rh != "" {
			_, _ = fmt.Sscanf(rh, "bytes=%d-", &start)
		}
		rest := payload[start:]
		w.Header().Set("Content-Length", fmt.Sprint(len(rest)))
		if start > 0 {
			w.WriteHeader(http.StatusPartialContent)
		}
		_, _ = w.Write(rest)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "audio.m4a")
	result, err := Download(context.Background(), bili.NewClient(""), server.URL, path, nil)
	if err != nil {
		t.Fatalf("重试后仍失败：%v", err)
	}
	if result.Retried == 0 {
		t.Error("应当发生过重试")
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(payload) {
		t.Fatalf("重试后内容不一致，长度 %d，期望 %d", len(data), len(payload))
	}
}

func TestDownloadGivesUpAfterMaxAttempts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "audio.m4a")
	_, err := Download(context.Background(), bili.NewClient(""), server.URL, path, nil)
	if err == nil {
		t.Fatal("应当返回错误")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("错误信息里应包含状态码：%v", err)
	}
}

func TestDownloadHonorsContextCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10485760")
		_, _ = w.Write(make([]byte, 1024))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	path := filepath.Join(t.TempDir(), "audio.m4a")
	if _, err := Download(ctx, bili.NewClient(""), server.URL, path, nil); err == nil {
		t.Fatal("取消后应返回错误")
	}
}
