#!/usr/bin/env bash
# 诊断脚本：直接测本机正在运行的 biliaudio 服务，绕开浏览器缓存等因素。
#
#   用法：./scripts/diagnose.sh [地址] [令牌]
#   例如：./scripts/diagnose.sh http://127.0.0.1:8080 5e13ed164274019fec67a880
#
# 每一步都会打印「请求 → 结果」，并把最关键的信息放在最后。

set -u

BASE="${1:-http://127.0.0.1:8080}"
TOKEN="${2:-}"
LINK='https://www.bilibili.com/video/BV1iF411j7oe/?spm_id_from=333.1007.top_right_bar_window_default_collection.content.click&vd_source=10d0f86227f3c318f8237345caac47c8'

if [ -n "$TOKEN" ]; then
  Q="?token=$TOKEN"
  SEP='&'
else
  Q=""
  SEP='?'
fi

hr() { printf '%s\n' "------------------------------------------------------------"; }

hr
echo "目标服务 : $BASE"
echo "令牌     : ${TOKEN:-（未提供，若服务要令牌则后续都会 403）}"
hr

echo "① /healthz（确认版本与启动时间）"
curl -s -m 10 "$BASE/healthz" || echo "  ✗ 连不上服务"
echo
hr

echo "② 首页：输入框初始值是否为空（旧版会是一个换行）"
echo "  请求: $BASE/$Q"
PAGE=$(curl -s -m 10 -w '\n__STATUS__%{http_code}' "$BASE/$Q")
STATUS="${PAGE##*__STATUS__}"
PAGE="${PAGE%__STATUS__*}"
echo "  状态: $STATUS"
if [ "$STATUS" = "403" ]; then
  echo "  ✗ 403 未授权：**令牌和服务端当前使用的那个不一致**。"
  echo "     最常见的原因：启动时没带 -token，服务自动生成了随机令牌，"
  echo "     而你用的还是上一次启动的那个。请用最新启动横幅里打印的令牌。"
elif [ -z "$PAGE" ]; then
  echo "  ✗ 首页无内容：服务可能没在运行"
else
  printf '%s' "$PAGE" | python3 -c '
import re, sys
html = sys.stdin.read()
m = re.search(r"<textarea[^>]*>([\s\S]*?)</textarea>", html)
if not m:
    print("  ✗ 页面里找不到输入框（可能不是本应用返回的页面）")
else:
    value = m.group(1)
    print(f"  输入框初始值 = {value!r}")
    print("  ✓ 正常" if value == "" else "  ✗ 非空：说明跑的是**修复前**的版本")
'
fi
hr

echo "③ 解析你给的链接（模拟浏览器 fetch 提交）"
BODY=$(curl -s -m 60 -X POST -H 'X-Requested-With: fetch' \
  --data-urlencode "input=$LINK" "$BASE/api/parse$Q")
printf '%s' "$BODY" | python3 -c '
import re, sys
html = sys.stdin.read()
if not html.strip():
    print("  ✗ 响应为空（多半是 403 未授权）")
    raise SystemExit
title = re.search(r"video-title\">([^<]*)", html)
err = re.search(r"is-error\"[^>]*>[\s\S]*?</svg>\s*([^<]*)", html)
if title:
    print(f"  ✓ 解析成功：{title.group(1)}")
else:
    print(f"  ✗ 解析失败：{(err.group(1).strip() if err else html[:200])}")
'
hr

echo "④ 若上面失败，再测一次不上送 input 的请求，用于区分「服务端判空」与「解析失败」"
curl -s -m 20 -X POST -H 'X-Requested-With: fetch' --data-binary 'input=' \
  "$BASE/api/parse$Q" | grep -oE '请输入视频链接[^<]*|未识别到[^<]*|video-title">[^<]*' | head -1 | sed 's/^/  /'
hr

echo
echo "把以上完整输出贴回来即可（不会泄露你的 Cookie，脚本只访问本机服务）。"
