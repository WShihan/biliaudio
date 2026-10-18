package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/WShihan/biliaudio/internal/bili"
	"github.com/WShihan/biliaudio/internal/buildinfo"
	"github.com/WShihan/biliaudio/internal/download"
	"github.com/WShihan/biliaudio/internal/input"
)

// Version 由构建时注入，见 Makefile 的 LDFLAGS。保留此变量以兼容旧脚本。
var Version = "dev"

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

type config struct {
	outDir   string
	template string
	pages    string
	quiet    bool
	dryRun   bool
	force    bool
	cookie   string
	timeout  time.Duration
	showVer  bool
	inputs   []string
}

// Run 是 CLI 入口，返回进程退出码。
//
// 默认行为是下载；`biliaudio serve` 会启动网页版。
func Run(args []string) int {
	if isServeCommand(args) {
		return runServe(args[1:])
	}

	console := NewConsole()

	cfg, err := parseFlags(console, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		console.Error("%v", err)
		return exitUsage
	}
	if cfg.showVer {
		fmt.Fprintf(console.Out, "biliaudio %s\n", buildinfo.Summary())
		fmt.Fprintf(console.Out, "%s\n", buildinfo.Runtime())
		return exitOK
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	text, err := collectInput(console, cfg)
	if err != nil {
		console.Error("%v", err)
		return exitUsage
	}

	extractor := input.NewExtractor()
	ids, extractErr := extractor.Extract(ctx, text)
	if extractErr != nil {
		// 短链解析失败不算致命，继续处理已识别到的 ID
		console.Warn("%v", extractErr)
	}
	if len(ids) == 0 {
		console.Error("没有识别到视频链接或 BV/av 号")
		console.Dim("  用法示例：biliaudio BV1GJ411x7h7")
		console.Dim("  也支持：biliaudio \"https://b23.tv/xxxxx\" 或管道输入")
		return exitError
	}

	client := bili.NewClient(cfg.cookie)
	selected, err := parsePageSelection(cfg.pages)
	if err != nil {
		console.Error("%v", err)
		return exitUsage
	}

	summary := &runSummary{}
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		processVideo(ctx, console, client, cfg, selected, id, summary)
	}

	printSummary(console, cfg, summary)
	if summary.failed > 0 {
		return exitError
	}
	return exitOK
}

// processVideo 解析单个视频并下载选中的分 P。
func processVideo(
	ctx context.Context,
	console *Console,
	client *bili.Client,
	cfg config,
	selected map[int]bool,
	id string,
	summary *runSummary,
) {
	video, err := client.FetchVideo(ctx, id)
	if err != nil {
		console.Error("%s：%v", id, err)
		summary.failed++
		return
	}

	printVideoInfo(console, video)

	parts := filterParts(video, selected)
	if len(parts) == 0 {
		console.Warn("%s：没有匹配的分 P（该视频共 %d 个）", id, len(video.Parts))
		summary.failed++
		return
	}

	for _, part := range parts {
		if ctx.Err() != nil {
			return
		}
		downloadPart(ctx, console, client, cfg, video, part, summary)
	}
}

// downloadPart 解析直链并下载单个分 P。
func downloadPart(
	ctx context.Context,
	console *Console,
	client *bili.Client,
	cfg config,
	video *bili.Video,
	part bili.Part,
	summary *runSummary,
) {
	name := buildName(cfg.template, partNameData(video, part))
	path := outputPath(cfg.outDir, name)

	streamURL, err := client.AudioURL(ctx, video.Bvid, part.Cid)
	if err != nil {
		console.Error("%s：%v", part.Name, err)
		summary.failed++
		return
	}

	if cfg.dryRun {
		console.Info("  %s", part.Name)
		console.Field("    输出", path)
		console.Dim("    直链 %s…", shorten(streamURL, 72))
		summary.skipped++
		return
	}

	if !cfg.force {
		if size, err := existingFileSize(path); err == nil && size > 0 {
			console.Dim("  跳过（已存在）：%s", path)
			summary.skipped++
			return
		}
	}

	console.Info("  %s", console.paint(ansiBlue, part.Name))
	console.Dim("    → %s", path)

	progress := console.NewProgress(part.Name)
	if cfg.quiet {
		progress = nil
	}

	result, err := download.Download(ctx, client, streamURL, path, func(written, total int64) {
		if progress != nil {
			progress.Update(written, total)
		}
	})
	if progress != nil {
		progress.Done(result.Bytes, result.Total)
	}

	if err != nil {
		if errors.Is(err, context.Canceled) {
			console.Warn("已中断，已下载部分保留在 %s.part，可重跑续传", path)
			summary.failed++
			return
		}
		console.Error("%s：%v", part.Name, err)
		summary.failed++
		return
	}

	note := ""
	if result.Resumed {
		note = "（续传）"
	}
	console.Success("    ✓ %s%s  用时 %s", humanBytes(result.Bytes), note, humanDuration(result.Elapsed))
	summary.downloaded++
	summary.bytes += result.Bytes
}

type runSummary struct {
	downloaded int
	skipped    int
	failed     int
	bytes      int64
}

func printSummary(console *Console, cfg config, summary *runSummary) {
	if cfg.dryRun || cfg.quiet {
		return
	}
	if summary.downloaded == 0 && summary.skipped == 0 && summary.failed == 0 {
		return
	}
	console.Info("")
	note := ""
	if summary.skipped > 0 {
		note = fmt.Sprintf("（跳过已存在 %d 个）", summary.skipped)
	}
	console.Info("完成：下载 %d 个%s，失败 %d 个，共 %s",
		summary.downloaded, note, summary.failed, humanBytes(summary.bytes))
}

// printVideoInfo 打印视频元信息，让用户先确认解析对不对。
func printVideoInfo(console *Console, video *bili.Video) {
	console.Info("")
	console.Title(video.Title)
	console.Field("UP 主", video.Author)
	if !video.Pubdate.IsZero() {
		console.Field("发布", video.Pubdate.Local().Format("2006-01-02"))
	}
	console.Field("时长", bili.FormatDuration(int(video.Duration.Seconds())))
	console.Field("统计", fmt.Sprintf("%s 播放 · %s 点赞 · %s 投币 · %s 收藏",
		bili.FormatCount(video.Stats.View),
		bili.FormatCount(video.Stats.Like),
		bili.FormatCount(video.Stats.Coin),
		bili.FormatCount(video.Stats.Fav),
	))
	console.Field("BV", video.Bvid)
	if len(video.Parts) > 1 {
		console.Field("分 P", fmt.Sprintf("%d 个", len(video.Parts)))
	}
}

// filterParts 按选择结果过滤分 P；未指定时返回全部。
func filterParts(video *bili.Video, selected map[int]bool) []bili.Part {
	if len(selected) == 0 {
		return video.Parts
	}
	var parts []bili.Part
	for _, part := range video.Parts {
		if selected[part.Index] || selected[part.Page] {
			parts = append(parts, part)
		}
	}
	return parts
}

// parsePageSelection 解析 "1,3-5" 形式的分 P 选择。
func parsePageSelection(spec string) (map[int]bool, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	selected := map[int]bool{}
	for _, chunk := range strings.Split(spec, ",") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		if start, end, ok := strings.Cut(chunk, "-"); ok {
			from, err := strconv.Atoi(strings.TrimSpace(start))
			if err != nil {
				return nil, fmt.Errorf("分 P 范围 %q 无法解析", chunk)
			}
			to, err := strconv.Atoi(strings.TrimSpace(end))
			if err != nil {
				return nil, fmt.Errorf("分 P 范围 %q 无法解析", chunk)
			}
			if from > to {
				from, to = to, from
			}
			for i := from; i <= to; i++ {
				selected[i] = true
			}
			continue
		}
		value, err := strconv.Atoi(chunk)
		if err != nil {
			return nil, fmt.Errorf("分 P %q 无法解析", chunk)
		}
		selected[value] = true
	}
	return selected, nil
}

// collectInput 汇总命令行参数与管道输入。
func collectInput(console *Console, cfg config) (string, error) {
	if len(cfg.inputs) > 0 {
		return strings.Join(cfg.inputs, "\n"), nil
	}
	if isTerminal(os.Stdin) {
		return "", errors.New("没有输入：请给出 BV 号/链接，或用管道把文本传进来")
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return "", fmt.Errorf("读取管道输入失败：%w", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "", errors.New("管道输入为空")
	}
	console.Dim("从标准输入读取了 %d 字节", len(text))
	return text, nil
}

func parseFlags(console *Console, args []string) (config, error) {
	cfg := config{}

	flags := flag.NewFlagSet("biliaudio", flag.ContinueOnError)
	flags.SetOutput(console.Err)
	flags.Usage = func() { printUsage(console) }

	flags.StringVar(&cfg.outDir, "o", ".", "输出目录")
	flags.StringVar(&cfg.outDir, "output", ".", "输出目录（同 -o）")
	flags.StringVar(&cfg.template, "f", defaultTemplate, "文件名模板，可用变量见下方说明")
	flags.StringVar(&cfg.template, "filename", defaultTemplate, "文件名模板（同 -f）")
	flags.StringVar(&cfg.pages, "p", "", "只下载指定分 P，如 -p 1 或 -p 1,3-5")
	flags.BoolVar(&cfg.quiet, "q", false, "安静模式：不显示进度条")
	flags.BoolVar(&cfg.dryRun, "n", false, "只解析不下载，查看将保存到哪里")
	flags.BoolVar(&cfg.dryRun, "dry-run", false, "只解析不下载（同 -n）")
	flags.BoolVar(&cfg.force, "force", false, "已存在同名文件时也重新下载")
	flags.StringVar(&cfg.cookie, "cookie", os.Getenv("BILI_COOKIE"), "B 站登录态 Cookie（默认读环境变量 BILI_COOKIE）")
	flags.DurationVar(&cfg.timeout, "timeout", 5*time.Minute, "整体超时，0 表示不限制")
	flags.BoolVar(&cfg.showVer, "version", false, "显示版本号")

	flags.Usage = func() { printUsage(console) }

	if err := flags.Parse(args); err != nil {
		return cfg, err
	}
	cfg.inputs = flags.Args()

	if cfg.timeout < 0 {
		return cfg, errors.New("-timeout 不能为负数")
	}
	return cfg, nil
}

func printUsage(console *Console) {
	fmt.Fprint(console.Err, `biliaudio - 下载 B 站视频音频的命令行工具

用法:
  biliaudio [选项] <BV号|av号|链接|包含链接的文本> ...
  biliaudio serve [选项]        启动网页版（静态资源已内嵌）

选项:
  -o, -output <目录>   输出目录（默认当前目录）
  -f, -filename <模板> 文件名模板（默认 "`+defaultTemplate+`"）
  -p <范围>            只下载指定分 P，如 -p 1 或 -p 1,3-5
  -n, -dry-run         只解析不下载
      -force           已存在同名文件时也重新下载
  -q                   安静模式（不显示进度条）
      -cookie <Cookie>  B 站登录态，默认读环境变量 BILI_COOKIE
      -timeout <时长>    整体超时（默认 5m，0 表示不限制）
      -version          显示版本号

模板变量:
  {bvid}    BV 号            {title}   标题（分 P 为分 P 名）
  {author}  UP 主            {page}    分 P 序号（单 P 为空）
  {part}    分 P 名           {index}   本次下载的序号
  {date}    发布日期

示例:
  biliaudio BV1GJ411x7h7
  biliaudio -o ~/Music -f "{author} - {title}" "https://www.bilibili.com/video/BV1GJ411x7h7"
  biliaudio BV1GJ411x7h7 BV1xx411c7mD   # 一次多个
  cat links.txt | biliaudio -o ~/Music  # 管道输入
`)
}

func existingFileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// shorten 截断长字符串用于展示。
func shorten(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit]
}
