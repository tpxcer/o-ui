#!/bin/sh
set -eu

# 提示按LC_ALL/LC_MESSAGES/LANG选择简体中文或英文（zh开头为中文，其余为英文），与程序一致。
case "${LC_ALL:-${LC_MESSAGES:-${LANG:-}}}" in
  zh*|ZH*) o_ui_zh=1 ;;
  *) o_ui_zh=0 ;;
esac
say() {
  if [ "$o_ui_zh" = 1 ]; then printf '%s\n' "$1" >&2; else printf '%s\n' "$2" >&2; fi
}

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  say '用法：o-ui /数据目录的绝对路径 [本机端口，默认5180]' 'Usage: o-ui /absolute/data/directory [local port, default 5180]'
  exit 1
fi
panel_data=$1
case "$panel_data" in
  /) say '不能把文件系统根目录用作测试数据目录。' 'The file system root cannot be used as the data directory.'; exit 1 ;;
  /*) ;;
  *) say '请明确指定独立测试数据目录的绝对路径。' 'Specify the absolute path of a dedicated data directory.'; exit 1 ;;
esac
panel_port=${2:-5180}
case "$panel_port" in
  ''|*[!0-9]*) say '端口须为1024至65535之间的数字。' 'Port must be a number from 1024 to 65535.'; exit 1 ;;
esac
if [ "${#panel_port}" -gt 5 ] || [ "$panel_port" -lt 1024 ] || [ "$panel_port" -gt 65535 ]; then
  say '端口须为1024至65535之间的数字。' 'Port must be a number from 1024 to 65535.'
  exit 1
fi
panel_bundle=$(CDPATH= cd -P "$(dirname "$0")" && pwd)
if [ ! -f "$panel_bundle/SHA256SUMS" ]; then
  say '本机构建包尚未完成，不能启动；请重新构建到新目录。' 'This package is incomplete and cannot start; rebuild into a new directory.'
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$panel_bundle" && sha256sum -c SHA256SUMS >/dev/null) || exit 1
elif command -v shasum >/dev/null 2>&1; then
  (cd "$panel_bundle" && shasum -a 256 -c SHA256SUMS >/dev/null) || exit 1
else
  say '缺少SHA256校验工具，尚未启动面板。' 'No SHA256 tool found; panel not started.'
  exit 1
fi
if [ ! -f "$panel_bundle/PLATFORM" ]; then
  say '缺少目标平台信息，尚未启动面板；请重新构建到新目录。' 'Target platform info is missing; panel not started, rebuild into a new directory.'
  exit 1
fi
panel_target=$(cat "$panel_bundle/PLATFORM")
case "$panel_target" in
  darwin-amd64|darwin-arm64|linux-amd64|linux-arm64) ;;
  *) say '构建目标平台无效，尚未启动面板。' 'Invalid build target platform; panel not started.'; exit 1 ;;
esac
case "$(uname -s)" in
  Darwin) panel_os=darwin ;;
  Linux) panel_os=linux ;;
  *) say '仅支持macOS/Linux测试环境，尚未启动面板。' 'Only macOS/Linux are supported; panel not started.'; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) panel_arch=amd64 ;;
  arm64|aarch64) panel_arch=arm64 ;;
  *) say '仅支持ARM64或x86_64，尚未启动面板。' 'Only ARM64 or x86_64 is supported; panel not started.'; exit 1 ;;
esac
if [ "$panel_target" != "$panel_os-$panel_arch" ]; then
  say "安装包目标为${panel_target}，当前系统为${panel_os}-${panel_arch}；平台不匹配，尚未启动面板。" "Package targets ${panel_target} but this system is ${panel_os}-${panel_arch}; platform mismatch, panel not started."
  exit 1
fi
say '正在启动 o-ui；使用已保存的访问设置（未设置时仅本机访问）。' 'Starting o-ui; saved access settings are used (local only until configured).'
exec "$panel_bundle/bin/o-ui" -listen "127.0.0.1:$panel_port" -data "$panel_data/core" -state "$panel_data/panel" -dist "$panel_bundle/dist"
