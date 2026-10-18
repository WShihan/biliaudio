package cli

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/WShihan/biliaudio/internal/bili"
)

const (
	defaultTemplate = "{page} - {title}"
	maxNameLength   = 120
)

var (
	// 文件名里保留：中日韩文字、字母数字、空格与常见符号
	unsafeNameChars = regexp.MustCompile(
		`[^\p{Han}\p{Hiragana}\p{Katakana}\p{Hangul}a-zA-Z0-9\s\-_()\[\]【】]`)
	multiUnderscore = regexp.MustCompile(`_{2,}`)
	multiSpace      = regexp.MustCompile(`\s{2,}`)
)

// nameData 是模板变量集合。变量名用 {xxx} 形式，直接替换，不做表达式。
type nameData struct {
	Bvid   string
	Title  string
	Author string
	Page   string // 单 P 时为空
	Part   string
	Index  string
	Date   string
}

// buildName 按模板生成文件名（不含目录与扩展名）。
func buildName(template string, data nameData) string {
	replacer := strings.NewReplacer(
		"{bvid}", data.Bvid,
		"{title}", data.Title,
		"{author}", data.Author,
		"{page}", data.Page,
		"{part}", data.Part,
		"{index}", data.Index,
		"{date}", data.Date,
	)
	name := replacer.Replace(template)
	return sanitizeName(name)
}

// sanitizeName 清理非法字符与多余空白，并限制长度。
func sanitizeName(name string) string {
	name = unsafeNameChars.ReplaceAllString(name, "_")
	name = multiUnderscore.ReplaceAllString(name, "_")
	name = multiSpace.ReplaceAllString(name, " ")
	name = strings.Trim(name, " _-.")

	// 按字符（而非字节）截断，避免把多字节汉字切坏
	if runes := []rune(name); len(runes) > maxNameLength {
		name = strings.Trim(string(runes[:maxNameLength]), " _-.")
	}
	if name == "" {
		name = "bilibili_audio_" + time.Now().Format("20060102_150405")
	}
	// Windows 保留名简单规避
	switch strings.ToUpper(name) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "LPT1", "LPT2":
		name = "_" + name
	}
	return name
}

// outputPath 组合最终输出路径，保证扩展名为 .m4a。
func outputPath(dir, name string) string {
	if !strings.EqualFold(filepath.Ext(name), ".m4a") {
		name += ".m4a"
	}
	return filepath.Join(dir, name)
}

// partNameData 由视频与分 P 组装模板变量。
func partNameData(video *bili.Video, part bili.Part) nameData {
	// 只有「确定是单 P」时才留空页码；video.Parts 缺失时也应保留真实页码
	page := "P" + itoa(part.Page)
	if len(video.Parts) == 1 {
		page = ""
	}
	date := ""
	if !video.Pubdate.IsZero() {
		date = video.Pubdate.Local().Format("2006-01-02")
	}
	return nameData{
		Bvid:   video.Bvid,
		Title:  part.Title,
		Author: video.Author,
		Page:   page,
		Part:   strings.TrimSpace(strings.TrimPrefix(part.Name, "P"+itoa(part.Page)+" · ")),
		Index:  itoa(part.Index),
		Date:   date,
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
