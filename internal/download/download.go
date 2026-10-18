// Package download 负责把音频流拉到本地：支持断点续传、失败重试与实时进度。
package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/WShihan/biliaudio/internal/bili"
)

const (
	// 单次请求超时；音频流较大，用较宽松的值
	requestTimeout = 15 * time.Minute
	bufferSize     = 256 << 10
	maxAttempts    = 4
	retryBaseDelay = 800 * time.Millisecond
)

// ProgressFunc 在下载过程中被反复调用。total 为 0 表示服务端未给出长度。
type ProgressFunc func(written, total int64)

// Result 一次下载的结果。
type Result struct {
	Path       string
	Bytes      int64
	Total      int64 // 0 表示未知
	Resumed    bool  // 是否由上次中断处继续
	Elapsed    time.Duration
	Retried    int
	Incomplete bool // 传输结束但字节数与 Content-Length 不符
}

// Download 下载 streamURL 到 path。中途失败会重试并自动续传，成功后原子落盘。
func Download(
	ctx context.Context,
	client *bili.Client,
	streamURL, path string,
	progress ProgressFunc,
) (Result, error) {
	started := time.Now()
	result := Result{Path: path}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return result, fmt.Errorf("创建目录失败：%w", err)
	}

	partPath := path + ".part"
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			result.Retried++
			if err := sleepCtx(ctx, retryBaseDelay*time.Duration(1<<(attempt-2))); err != nil {
				return result, err
			}
		}

		offset, err := fileSize(partPath)
		if err != nil {
			return result, err
		}
		if offset > 0 {
			result.Resumed = true
		}

		written, total, err := transfer(ctx, client, streamURL, partPath, offset, progress)
		result.Bytes = written
		result.Total = total
		result.Elapsed = time.Since(started)
		lastErr = err

		if err == nil && total > 0 && written != total {
			lastErr = fmt.Errorf("只收到 %d/%d 字节", written, total)
			err = lastErr
		}
		if err == nil {
			if renameErr := os.Rename(partPath, path); renameErr != nil {
				return result, fmt.Errorf("重命名临时文件失败：%w", renameErr)
			}
			result.Elapsed = time.Since(started)
			result.Incomplete = false
			return result, nil
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if attempt == maxAttempts {
			if total > 0 && written != total {
				result.Incomplete = true
			}
			break
		}
	}

	result.Bytes, _ = fileSize(partPath)
	result.Elapsed = time.Since(started)
	return result, fmt.Errorf("下载失败（已重试 %d 次）：%w", result.Retried, lastErr)
}

// transfer 执行一次 HTTP 传输，返回本次会话累计写入的字节数与总长度。
func transfer(
	ctx context.Context,
	client *bili.Client,
	streamURL, partPath string,
	existing int64,
	progress ProgressFunc,
) (written, total int64, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header = client.Headers()
	if existing > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existing))
	}

	httpClient := &http.Client{Timeout: requestTimeout}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("请求音频流失败：%w", err)
	}
	defer resp.Body.Close()

	switch {
	case existing > 0 && resp.StatusCode == http.StatusPartialContent:
		// 正常续传
	case resp.StatusCode == http.StatusOK:
		// 服务端忽略了 Range，只能从头写
		existing = 0
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		// 本地文件已经比远端完整，直接当成下载完成
		size, sizeErr := fileSize(partPath)
		return size, size, sizeErr
	default:
		return 0, 0, fmt.Errorf("音频流返回 HTTP %d", resp.StatusCode)
	}

	total = parseTotal(resp, existing)
	flags := os.O_CREATE | os.O_WRONLY
	if existing == 0 {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(partPath, flags, 0o644)
	if err != nil {
		return 0, total, err
	}
	defer file.Close()

	if existing > 0 {
		if _, err := file.Seek(existing, io.SeekStart); err != nil {
			return existing, total, err
		}
	}

	written = existing
	if progress != nil {
		progress(written, total)
	}

	buffer := make([]byte, bufferSize)
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := file.Write(buffer[:n]); writeErr != nil {
				return written, total, fmt.Errorf("写入文件失败：%w", writeErr)
			}
			written += int64(n)
			if progress != nil {
				progress(written, total)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return written, total, fmt.Errorf("读取音频流失败：%w", readErr)
		}
	}
	return written, total, nil
}

// parseTotal 结合响应头与续传偏移算出音频总长度，未知时返回 0。
func parseTotal(resp *http.Response, existing int64) int64 {
	if resp.ContentLength > 0 {
		return existing + resp.ContentLength
	}
	if resp.ContentLength == 0 {
		return existing
	}
	return 0
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
