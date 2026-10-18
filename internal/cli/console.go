// Package cli 实现 biliaudio 命令行界面。
package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	ansiReset  = "\033[0m"
	ansiDim    = "\033[2m"
	ansiBold   = "\033[1m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiBlue   = "\033[36m"
)

// Console 负责所有终端输出，统一处理颜色开关与「是否为终端」的判断。
type Console struct {
	Out   io.Writer
	Err   io.Writer
	color bool

	mu sync.Mutex
}

// NewConsole 创建输出器；当输出被重定向到文件时自动关闭颜色与动态进度。
func NewConsole() *Console {
	return &Console{
		Out:   os.Stdout,
		Err:   os.Stderr,
		color: colorEnabled(os.Stdout),
	}
}

func colorEnabled(w io.Writer) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTerminal(w)
}

func isTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// IsTTY 判断 stdout 是否为终端（决定是否输出动态进度条）。
func (c *Console) IsTTY() bool { return isTerminal(c.Out) }

func (c *Console) paint(code, text string) string {
	if !c.color || text == "" {
		return text
	}
	return code + text + ansiReset
}

// Title 输出标题。
func (c *Console) Title(text string) { c.println(c.paint(ansiBold, text)) }

// Dim 输出次要信息。
func (c *Console) Dim(format string, args ...any) {
	c.println(c.paint(ansiDim, fmt.Sprintf(format, args...)))
}

// Info 输出普通信息。
func (c *Console) Info(format string, args ...any) {
	c.println(fmt.Sprintf(format, args...))
}

// Success 输出成功信息。
func (c *Console) Success(format string, args ...any) {
	c.println(c.paint(ansiGreen, fmt.Sprintf(format, args...)))
}

// Warn 输出警告信息（到 stderr）。
func (c *Console) Warn(format string, args ...any) {
	c.eprintln(c.paint(ansiYellow, "警告: "+fmt.Sprintf(format, args...)))
}

// Error 输出错误信息（到 stderr）。
func (c *Console) Error(format string, args ...any) {
	c.eprintln(c.paint(ansiRed, "错误: "+fmt.Sprintf(format, args...)))
}

// Field 输出「标签: 值」形式，标签右对齐以保持整齐。
func (c *Console) Field(label, value string) {
	c.println(fmt.Sprintf("  %s %s", c.paint(ansiDim, label+":"), value))
}

func (c *Console) println(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Fprintln(c.Out, text)
}

func (c *Console) eprintln(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Fprintln(c.Err, text)
}

// Progress 是单行动态进度条；非终端环境下自动退化为「开始/结束」两行。
type Progress struct {
	console *Console
	label   string
	start   time.Time
	last    time.Time

	interactive bool
	finished    bool
	lastWidth   int
}

// NewProgress 创建进度条。total 为 0 表示长度未知。
func (c *Console) NewProgress(label string) *Progress {
	return &Progress{
		console:     c,
		label:       label,
		start:       time.Now(),
		last:        time.Now(),
		interactive: c.IsTTY(),
	}
}

// Update 刷新进度。total 为 0 时只显示已下载量与速度。
func (p *Progress) Update(written, total int64) {
	if !p.interactive {
		return
	}
	// 限制刷新频率，避免大文件时刷屏
	now := time.Now()
	if now.Sub(p.last) < 120*time.Millisecond && (total == 0 || written < total) {
		return
	}
	p.last = now
	p.render(written, total, false)
}

// Done 结束进度条并换行。
func (p *Progress) Done(written, total int64) {
	if p.finished {
		return
	}
	p.finished = true
	if p.interactive {
		p.render(written, total, true)
		p.console.mu.Lock()
		fmt.Fprintln(p.console.Out)
		p.console.mu.Unlock()
	}
}

func (p *Progress) render(written, total int64, final bool) {
	elapsed := time.Since(p.start).Seconds()
	speed := ""
	if elapsed > 0.2 {
		speed = " " + humanBytes(int64(float64(written)/elapsed)) + "/s"
	}

	var line string
	if total > 0 {
		ratio := float64(written) / float64(total)
		if ratio > 1 {
			ratio = 1
		}
		bar := renderBar(ratio, 24)
		line = fmt.Sprintf("\r  %s %s %s %s%s",
			bar,
			humanBytes(written)+"/"+humanBytes(total),
			fmt.Sprintf("%3.0f%%", ratio*100),
			p.label,
			speed,
		)
	} else {
		line = fmt.Sprintf("\r  %s %s%s", humanBytes(written), p.label, speed)
	}

	p.console.mu.Lock()
	defer p.console.mu.Unlock()
	fmt.Fprint(p.console.Out, line)
	if width := visibleLen(line); p.lastWidth > width {
		fmt.Fprint(p.console.Out, strings.Repeat(" ", p.lastWidth-width))
	}
	if final {
		p.lastWidth = 0
		return
	}
	p.lastWidth = visibleLen(line)
}

func renderBar(ratio float64, width int) string {
	if width <= 0 {
		return ""
	}
	filled := int(ratio*float64(width) + 0.5)
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("=", filled) + strings.Repeat("-", width-filled) + "]"
}

func visibleLen(s string) int {
	// 进度条里只有 ASCII 与一个 \r
	return len(s) - 1
}

// humanBytes 把字节数格式化成易读形式。
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for value := n / unit; value >= unit && exp < 4; value /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}

// humanDuration 把秒数格式化成 1m20s 这类形式。
func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
