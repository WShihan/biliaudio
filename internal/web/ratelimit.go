package web

import (
	"sync"
	"time"
)

// rateLimiter 是简单的固定窗口计数限流，按客户端 IP 统计。
//
// 服务会暴露在公网上（手机访问），没有限流的话一个脚本就能把 B 站接口
// 配额刷爆，甚至导致出口 IP 被风控。
type rateLimiter struct {
	limit  int
	window time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	count int
	reset time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		limit:   limit,
		window:  window,
		buckets: make(map[string]*bucket),
	}
}

// allow 判断该 IP 本次请求是否放行。
func (l *rateLimiter) allow(key string) bool {
	if l.limit <= 0 {
		return true
	}
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	// 顺手清理过期窗口，避免 map 无限增长
	if len(l.buckets) > 4096 {
		for k, b := range l.buckets {
			if now.After(b.reset) {
				delete(l.buckets, k)
			}
		}
	}

	current, ok := l.buckets[key]
	if !ok || now.After(current.reset) {
		l.buckets[key] = &bucket{count: 1, reset: now.Add(l.window)}
		return true
	}
	if current.count >= l.limit {
		return false
	}
	current.count++
	return true
}
