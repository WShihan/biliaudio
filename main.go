// Command biliaudio 下载 B 站视频的音频。
//
// 它把网页版那套「解析 + 代理转发」变成一个纯本地的命令行工具：
// 用你自己的网络直连 B 站接口与音频 CDN，因此不受机房 IP 风控影响。
package main

import (
	"os"

	"github.com/WShihan/biliaudio/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
