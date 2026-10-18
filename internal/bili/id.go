package bili

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// BV 号固定 12 位：BV + 10 位 base58 字符，且区分大小写
	bvPattern = regexp.MustCompile(`(?i)BV[0-9A-Za-z]{10}`)
	avPattern = regexp.MustCompile(`(?i)(?:^|[^0-9A-Za-z])av(\d+)(?:[^0-9A-Za-z]|$)`)
)

// Bvid 从任意字符串中取出 BV 号，正字法无关（bv1xx 也可）。
// 第二个返回值表示是否找到。
func Bvid(s string) (string, bool) {
	match := bvPattern.FindString(s)
	if match == "" {
		return "", false
	}
	return BvidNormalize(match), true
}

// Avid 从任意字符串中取出 av 号对应的 aid（不含 "av" 前缀）。
func Avid(s string) (string, bool) {
	match := avPattern.FindStringSubmatch(s)
	if len(match) < 2 {
		return "", false
	}
	if _, err := strconv.ParseInt(match[1], 10, 64); err != nil {
		return "", false
	}
	return match[1], true
}

// BvidNormalize 只修正 "bv" 前缀的大小写。
// BV 号本体是大小写敏感的，"BV1GJ411x7h7" 与 "BV1gj411x7h7" 是两个不同的视频，
// 因此绝不能整体 ToUpper。
func BvidNormalize(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return s
	}
	return "BV" + s[2:]
}

// FormatCount 把统计数字格式化成「1.2万」这类中文习惯写法。
func FormatCount(n int64) string {
	switch {
	case n >= 100_000_000:
		return trimZero(float64(n)/100_000_000, "亿")
	case n >= 10_000:
		return trimZero(float64(n)/10_000, "万")
	default:
		return strconv.FormatInt(n, 10)
	}
}

func trimZero(value float64, unit string) string {
	text := strconv.FormatFloat(value, 'f', 1, 64)
	text = strings.TrimSuffix(text, ".0")
	return text + unit
}

// FormatDuration 把时长格式化成 "3:33" 或 "1:02:03"。
func FormatDuration(d int) string {
	if d < 0 {
		d = 0
	}
	hours := d / 3600
	minutes := (d % 3600) / 60
	seconds := d % 60
	if hours > 0 {
		return strconv.Itoa(hours) + ":" + pad2(minutes) + ":" + pad2(seconds)
	}
	return strconv.Itoa(minutes) + ":" + pad2(seconds)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
