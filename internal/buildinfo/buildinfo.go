// Package buildinfo 提供构建版本信息。
//
// 线上排查最怕「改完没生效却看不出来」，因此把版本暴露到启动横幅与 /healthz，
// 一眼就能确认跑的是不是刚编译出来的那个二进制。
package buildinfo

import (
	"fmt"
	"runtime"
	"time"
)

var (
	// Version 由 -ldflags 注入，如 v1.0.0
	Version = "dev"
	// Commit 由 -ldflags 注入
	Commit = ""
	// Date 由 -ldflags 注入
	Date = ""
)

// Summary 返回一行版本描述。
func Summary() string {
	text := Version
	if Commit != "" && Commit != Version {
		text += " (" + Commit + ")"
	}
	if Date != "" {
		text += " " + Date
	}
	return text
}

// Runtime 返回 Go 版本与目标平台。
func Runtime() string {
	return fmt.Sprintf("%s %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// Health 返回给 /healthz 的结构化版本信息。
func Health() map[string]string {
	info := map[string]string{
		"version": Version,
		"go":      runtime.Version(),
		"os":      runtime.GOOS,
		"arch":    runtime.GOARCH,
	}
	if Commit != "" {
		info["commit"] = Commit
	}
	if Date != "" {
		info["date"] = Date
	}
	if StartedAt != "" {
		info["started"] = StartedAt
	}
	return info
}

// StartedAt 是进程启动时间，用于判断服务是否真的重启过。
var StartedAt = time.Now().Format("2006-01-02 15:04:05")
