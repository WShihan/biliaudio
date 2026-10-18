package bili

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
)

// MediaSuffixes 是可以代理转发的 B 站媒体域名后缀。
//
// 网页端由服务端代理音频流，如果不校验目标域名，任何人都能拿它当跳板去访问
// 内网或云元数据服务（SSRF）。这里只放行 B 站自家 CDN。
var MediaSuffixes = []string{
	".bilivideo.com",
	".bilivideo.cn",
	".hdslb.com",
	".bilibili.com",
	".akamaized.net",
}

// CheckMediaURL 校验音频直链是否属于允许代理的域名。
func CheckMediaURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("音频地址无法解析：%w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("音频地址协议不支持：%q", parsed.Scheme)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return fmt.Errorf("音频地址缺少主机名：%q", raw)
	}
	for _, suffix := range MediaSuffixes {
		if host == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(host, suffix) {
			return nil
		}
	}
	return fmt.Errorf("音频域名不在允许列表内：%q", host)
}

// EncodeToken 把音频直链编成 URL 安全的 base64，用于放进代理地址的路径。
func EncodeToken(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeToken 还原 EncodeToken 的结果。
func DecodeToken(token string) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(token, "="))
	if err != nil {
		return "", fmt.Errorf("音频地址编码无效：%w", err)
	}
	return string(data), nil
}
