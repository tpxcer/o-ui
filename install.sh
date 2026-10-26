#!/usr/bin/env bash
# o-ui 安装与管理：安装、升级、卸载、HTTPS证书（acme.sh/Let's Encrypt）与BBR。
# 仅支持使用systemd的Linux amd64/arm64；首次安装后本脚本即/usr/local/bin/o-ui。
# 不修改其他防火墙规则；所有系统改动均先询问。
set -euo pipefail
umask 022

REPO=${O_UI_REPO:-tpxcer/o-ui}
ACME_SH_VERSION=3.1.6
# GitHub官方标签源码包摘要（2026-10-20两次下载核对一致）
ACME_SH_SHA256=0d3f9000ac44a6331314742a88c475f79134e24fc991997883652adc59efc486
HTTPS_CONFIRM='确认启用HTTPS非本机访问'
RECOVERY_CONFIRM='恢复面板本机访问'
SERVICE_USER=o-ui
# 仅供测试把全部系统路径放到临时根目录；正式运行为空。
R=${O_UI_ROOT_PREFIX:-}

P_INSTALL=/opt/o-ui
P_DATA=/var/lib/o-ui
P_ETC=/etc/o-ui
P_TLS=$P_ETC/tls
P_ACME=$P_ETC/acme
P_CMD=/usr/local/bin/o-ui
P_HELPER=/usr/local/libexec/o-ui-firewall
P_FWRULES=/var/lib/o-ui-firewall/rules
P_UPDATE=/usr/local/libexec/o-ui-update
P_UPDATESTATE=/var/lib/o-ui-update
F_INSTALL=$R$P_INSTALL
F_DATA=$R$P_DATA
F_ETC=$R$P_ETC
F_TLS=$R$P_TLS
F_ACME=$R$P_ACME
F_CMD=$R$P_CMD
F_HELPER=$R$P_HELPER
F_FWRULES=$R$P_FWRULES
F_UPDATE=$R$P_UPDATE
F_UPDATESTATE=$R$P_UPDATESTATE
F_SUDOERS=$R/etc/sudoers.d/o-ui
F_CONF=$F_ETC/o-ui.conf
F_UNITS=$R/etc/systemd/system
F_SYSCTL=$R/etc/sysctl.d/99-o-ui-bbr.conf
F_MODULES=$R/etc/modules-load.d/o-ui-bbr.conf
F_PROC=$R/proc

CONF_KEYS='OUI_LANG OUI_PORT OUI_PATH OUI_MODE OUI_HOST OUI_CHALLENGE OUI_HTTP_SERVICE OUI_RENEW OUI_VERSION OUI_FW OUI_BBR_PREV_QDISC OUI_BBR_PREV_CC OUI_BBR_MODULE'
OUI_LANG= OUI_PORT= OUI_PATH= OUI_MODE= OUI_HOST= OUI_CHALLENGE= OUI_HTTP_SERVICE= OUI_RENEW= OUI_VERSION= OUI_FW= OUI_BBR_PREV_QDISC= OUI_BBR_PREV_CC= OUI_BBR_MODULE=
WORK=
PKG_DIR=
OPT_PACKAGE=
OPT_SUMS=
OPT_VERSION=

case ${LC_ALL:-${LC_MESSAGES:-${LANG:-}}} in zh*|ZH*) OUI_LANG=zh ;; *) OUI_LANG=en ;; esac

t() { if [ "$OUI_LANG" = en ]; then printf '%s' "$2"; else printf '%s' "$1"; fi; }
info() { printf '%s\n' "$(t "$1" "$2")"; }
warn() { printf '%s\n' "$(t "警告：$1" "Warning: $2")" >&2; }
die() { printf '%s\n' "$(t "错误：$1" "Error: $2")" >&2; exit 1; }
cleanup() { if [ -n "$WORK" ]; then rm -rf "$WORK"; fi; }
trap cleanup EXIT

# ---------- 交互 ----------

# 从终端读取回答；测试或自动化以O_UI_STDIN_ANSWERS=1从标准输入逐行提供。
stdin_check() {
  if [ -t 0 ] || [ -n "${O_UI_STDIN_ANSWERS:-}" ]; then return 0; fi
  if [ -r /dev/tty ]; then exec </dev/tty; return 0; fi
  die "需要交互终端；请用 bash <(curl -fsSL 安装脚本地址) 运行" "an interactive terminal is required; run with bash <(curl -fsSL <installer URL>)"
}

# ask 变量 中文提示 英文提示 [默认值]
ask() {
  local __var=$1 __def=${4-} __ans
  if [ -n "$__def" ]; then printf '%s [%s]: ' "$(t "$2" "$3")" "$__def"; else printf '%s: ' "$(t "$2" "$3")"; fi
  IFS= read -r __ans || die "输入已结束" "input ended"
  [ -n "$__ans" ] || __ans=$__def
  printf -v "$__var" '%s' "$__ans"
}

ask_secret() {
  local __var=$1 __ans
  printf '%s: ' "$(t "$2" "$3")"
  IFS= read -r -s __ans || die "输入已结束" "input ended"
  printf '\n'
  printf -v "$__var" '%s' "$__ans"
}

# ask_yes 中文 英文 默认y|n；是返回0
ask_yes() {
  local __ans __hint='[y/N]'
  [ "$3" = y ] && __hint='[Y/n]'
  while :; do
    printf '%s %s: ' "$(t "$1" "$2")" "$__hint"
    IFS= read -r __ans || die "输入已结束" "input ended"
    [ -n "$__ans" ] || __ans=$3
    case $__ans in y|Y|yes|YES) return 0 ;; n|N|no|NO) return 1 ;; esac
  done
}

# choose 变量 默认序号 最大序号 [最小序号，默认1]；选项文本须先由调用方打印
choose() {
  local __var=$1 __def=$2 __max=$3 __min=${4:-1} __ans
  while :; do
    printf '%s [%s]: ' "$(t '请输入序号' 'Enter a number')" "$__def"
    IFS= read -r __ans || die "输入已结束" "input ended"
    [ -n "$__ans" ] || __ans=$__def
    case $__ans in *[!0-9]*|'') continue ;; esac
    if [ "$__ans" -ge "$__min" ] && [ "$__ans" -le "$__max" ]; then printf -v "$__var" '%s' "$__ans"; return 0; fi
  done
}

select_lang() {
  printf '%s\n' '请选择语言 / Select language:' '  1) 简体中文' '  2) English'
  local n
  choose n 1 2
  if [ "$n" = 2 ]; then OUI_LANG=en; else OUI_LANG=zh; fi
}

# ---------- 环境与工具 ----------

need_root() { [ "$(id -u)" = 0 ] || die "请用root运行" "please run as root"; }

detect_arch() {
  [ "$(uname -s)" = Linux ] || die "仅支持Linux" "only Linux is supported"
  case $(uname -m) in
    x86_64|amd64) printf amd64 ;;
    aarch64|arm64) printf arm64 ;;
    *) die "仅支持x86_64(amd64)与aarch64(arm64)" "only x86_64 (amd64) and aarch64 (arm64) are supported" ;;
  esac
}

need_systemd() {
  [ -d "$R/run/systemd/system" ] && command -v systemctl >/dev/null 2>&1 || die "需要systemd" "systemd is required"
}

need_cmd() {
  local c
  for c in "$@"; do command -v "$c" >/dev/null 2>&1 || die "缺少命令：$c" "missing command: $c"; done
}

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --proto '=https' --tlsv1.2 --retry 3 --connect-timeout 20 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q --https-only -O "$2" "$1"
  else
    die "需要curl或wget" "curl or wget is required"
  fi
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi
}

check_sums_in() {
  if command -v sha256sum >/dev/null 2>&1; then (cd "$1" && sha256sum -c --quiet SHA256SUMS); else (cd "$1" && shasum -a 256 -c --quiet SHA256SUMS); fi
}

port_in_use() {
  command -v ss >/dev/null 2>&1 || return 1
  ss -ltn 2>/dev/null | awk -v p="$1" 'NR > 1 { n = split($4, a, ":"); if (a[n] == p) f = 1 } END { exit !f }'
}

as_service() {
  local loc=zh_CN.UTF-8
  [ "$OUI_LANG" = en ] && loc=en_US.UTF-8
  if command -v runuser >/dev/null 2>&1; then
    (cd / && env LC_ALL="$loc" runuser -u "$SERVICE_USER" -- "$@")
  elif command -v setpriv >/dev/null 2>&1; then
    (cd / && env LC_ALL="$loc" setpriv --reuid="$SERVICE_USER" --regid="$SERVICE_USER" --init-groups -- "$@")
  else
    die "需要runuser或setpriv" "runuser or setpriv is required"
  fi
}

pkg_install() {
  if command -v apt-get >/dev/null 2>&1; then apt-get install -y "$@"
  elif command -v dnf >/dev/null 2>&1; then dnf install -y "$@"
  elif command -v yum >/dev/null 2>&1; then yum install -y "$@"
  else return 1; fi
}

# 缺少时询问后用系统包管理器安装
ensure_tool() {
  command -v "$1" >/dev/null 2>&1 && return 0
  ask_yes "缺少$1，是否用系统包管理器安装$2？" "$1 is missing; install package $2 with the system package manager?" y || return 1
  pkg_install "$2" && command -v "$1" >/dev/null 2>&1
}

random_alnum() {
  local s=''
  while [ ${#s} -lt "$1" ]; do s=$s$(head -c 256 /dev/urandom | LC_ALL=C tr -dc 'A-Za-z0-9'); done
  printf '%s' "${s:0:$1}"
}

is_ipv4() { printf '%s' "$1" | grep -Eq '^([0-9]{1,3}\.){3}[0-9]{1,3}$'; }
is_ipv6() { case $1 in *:*) return 0 ;; *) return 1 ;; esac; }
is_ip() { is_ipv4 "$1" || is_ipv6 "$1"; }

# ---------- 配置 ----------

conf_load() {
  [ -f "$F_CONF" ] || return 0
  local key value
  while IFS='=' read -r key value; do
    case " $CONF_KEYS " in *" $key "*) ;; *) continue ;; esac
    case $value in *[!A-Za-z0-9._:@/-]*) die "配置文件含无效值：$key" "invalid value in config: $key" ;; esac
    printf -v "$key" '%s' "$value"
  done <"$F_CONF"
}

conf_save() {
  local key
  mkdir -p "$F_ETC"
  chmod 0755 "$F_ETC"
  (
    umask 077
    for key in $CONF_KEYS; do printf '%s=%s\n' "$key" "${!key}"; done >"$F_CONF.tmp"
  )
  mv "$F_CONF.tmp" "$F_CONF"
}

installed() { [ -e "$F_INSTALL/o-ui" ] || [ -f "$F_UNITS/o-ui.service" ]; }

require_installed() {
  need_root
  installed || die "o-ui尚未安装" "o-ui is not installed"
  conf_load
}

# ---------- 发布包 ----------

parse_pkg_opts() {
  while [ $# -gt 0 ]; do
    case $1 in
      --package) [ $# -ge 2 ] || die "--package缺少文件" "--package needs a file"; OPT_PACKAGE=$2; shift 2 ;;
      --sums) [ $# -ge 2 ] || die "--sums缺少文件" "--sums needs a file"; OPT_SUMS=$2; shift 2 ;;
      --version) [ $# -ge 2 ] || die "--version缺少版本" "--version needs a tag"; OPT_VERSION=$2; shift 2 ;;
      *) die "未知参数：$1" "unknown option: $1" ;;
    esac
  done
  case $OPT_VERSION in ''|v[0-9]*|[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]|[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]-v[0-9]*) ;; *) die "版本应形如2026-10-21或2026-10-21-v1" "version must look like 2026-10-21 or 2026-10-21-v1" ;; esac
  case $OPT_VERSION in *[!A-Za-z0-9.-]*) die "版本含无效字符" "invalid characters in version" ;; esac
}

# 下载/读取对应架构的包，核对发布清单与包内清单后解压到$PKG_DIR
prepare_package() {
  local arch name tgz sums expected actual entry
  arch=$(detect_arch)
  name=o-ui-linux-$arch.tar.gz
  WORK=$(mktemp -d)
  tgz=$WORK/$name
  sums=$WORK/SHA256SUMS
  if [ -n "$OPT_PACKAGE" ]; then
    [ -f "$OPT_PACKAGE" ] || die "找不到包：$OPT_PACKAGE" "package not found: $OPT_PACKAGE"
    cp "$OPT_PACKAGE" "$tgz"
    [ -n "$OPT_SUMS" ] || OPT_SUMS=$(dirname "$OPT_PACKAGE")/SHA256SUMS
    if [ -f "$OPT_SUMS" ]; then cp "$OPT_SUMS" "$sums"; else sums=; warn "未找到发布清单SHA256SUMS，只校验包内清单" "release SHA256SUMS not found; only the in-package manifest is checked"; fi
  else
    printf '%s' "$REPO" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || die "发布仓库无效（O_UI_REPO应为owner/name）" "invalid release repository (O_UI_REPO must be owner/name)"
    local base=https://github.com/$REPO/releases/latest/download
    [ -z "$OPT_VERSION" ] || base=https://github.com/$REPO/releases/download/$OPT_VERSION
    info "下载 $base/$name" "Downloading $base/$name"
    fetch "$base/$name" "$tgz" || die "下载失败" "download failed"
    fetch "$base/SHA256SUMS" "$sums" || die "下载发布清单失败" "failed to download SHA256SUMS"
  fi
  if [ -n "$sums" ]; then
    expected=$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1 }' "$sums")
    printf '%s' "$expected" | grep -Eq '^[0-9a-f]{64}$' || die "发布清单中没有唯一的$name摘要" "SHA256SUMS has no single digest for $name"
    actual=$(sha256_of "$tgz")
    [ "$actual" = "$expected" ] || die "$name摘要不符，已停止" "$name digest mismatch; aborted"
  fi
  while IFS= read -r entry; do
    case $entry in o-ui/*|o-ui) ;; *) die "包内路径无效：$entry" "invalid path in package: $entry" ;; esac
    case /$entry/ in */../*) die "包内路径无效：$entry" "invalid path in package: $entry" ;; esac
  done < <(tar -tzf "$tgz")
  mkdir "$WORK/pkg"
  tar -xzf "$tgz" -C "$WORK/pkg" --no-same-owner
  [ -z "$(find "$WORK/pkg" -type l -o ! -type f ! -type d)" ] || die "包内含链接或特殊文件" "package contains links or special files"
  PKG_DIR=$WORK/pkg/o-ui
  check_sums_in "$PKG_DIR" || die "包内清单校验失败" "in-package manifest check failed"
  [ "$(cat "$PKG_DIR/PLATFORM")" = "linux-$arch" ] || die "包平台与本机不符" "package platform does not match this machine"
  [ -f "$PKG_DIR/bin/o-ui" ] && [ -f "$PKG_DIR/o-ui" ] && [ -f "$PKG_DIR/dist/index.html" ] || die "包不完整" "package is incomplete"
}

pkg_version() { if [ -f "$1/VERSION" ]; then tr -cd 'A-Za-z0-9.-' <"$1/VERSION"; else printf unknown; fi; }

# 原子替换/opt/o-ui，旧版保留为.prev；同时更新管理命令
install_files() {
  local new=$F_INSTALL.new
  rm -rf "$new"
  mkdir -p "$(dirname "$F_INSTALL")"
  cp -R "$PKG_DIR" "$new"
  chown -R root:root "$new"
  chmod -R u=rwX,go=rX "$new"
  chmod 0755 "$new/o-ui" "$new/bin/o-ui"
  if [ -d "$F_INSTALL" ]; then rm -rf "$F_INSTALL.prev"; mv "$F_INSTALL" "$F_INSTALL.prev"; fi
  mv "$new" "$F_INSTALL"
  mkdir -p "$(dirname "$F_CMD")"
  if [ -f "$F_INSTALL/install.sh" ]; then cp "$F_INSTALL/install.sh" "$F_CMD.tmp"
  elif [ -f "$0" ]; then cp "$0" "$F_CMD.tmp"
  else die "包内缺少install.sh" "install.sh is missing from the package"; fi
  chmod 0755 "$F_CMD.tmp"
  mv "$F_CMD.tmp" "$F_CMD"
}

ensure_user() {
  local shell=/bin/false
  [ -x /usr/sbin/nologin ] && shell=/usr/sbin/nologin
  if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
    useradd --system --user-group --home-dir "$P_DATA" --no-create-home --shell "$shell" "$SERVICE_USER"
  fi
  mkdir -p "$F_DATA"
  chown "$SERVICE_USER:$SERVICE_USER" "$F_DATA"
  chmod 0700 "$F_DATA"
}

# ---------- systemd ----------

write_unit() {
  local loc=zh_CN.UTF-8 envs
  [ "$OUI_LANG" = en ] && loc=en_US.UTF-8
  envs="Environment=LANG=$loc
Environment=O_UI_DEFAULT_LANG=$OUI_LANG"
  # 升级时保留单元里其他已有的环境变量
  if [ -f "$F_UNITS/o-ui.service" ]; then
    envs="$envs$(grep '^Environment=' "$F_UNITS/o-ui.service" | grep -v -e '^Environment=LANG=' -e '^Environment=O_UI_DEFAULT_LANG=' | sed 's/^/\
/' || true)"
  fi
  mkdir -p "$F_UNITS"
  cat >"$F_UNITS/o-ui.service" <<EOF
[Unit]
Description=o-ui panel
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=120
StartLimitBurst=5

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
$envs
ExecStart=$P_INSTALL/o-ui $P_DATA $OUI_PORT
Restart=on-failure
RestartSec=5s
# 只向面板主进程发SIGTERM，由面板自行停止核心并保留“退出时核心在运行则下次自动恢复”的标记
KillMode=mixed
KillSignal=SIGTERM
TimeoutStopSec=30s
# 防火墙助手经sudo运行；只开放防火墙持久化目录，其他/etc内容保持只读。
PrivateTmp=yes
ProtectSystem=full
ReadWritePaths=-/etc/ufw
ReadWritePaths=-/etc/nftables.d
ProtectHome=yes

[Install]
WantedBy=multi-user.target
EOF
}

write_renew_units() {
  cat >"$F_UNITS/o-ui-cert-renew.service" <<EOF
[Unit]
Description=o-ui certificate renewal (acme.sh)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=$P_CMD cert-cron
ExecStopPost=$P_CMD cert-http-resume
TimeoutStartSec=30min
EOF
  cat >"$F_UNITS/o-ui-cert-renew.timer" <<EOF
[Unit]
Description=o-ui certificate renewal check every 6 hours

[Timer]
OnCalendar=*-*-* 00/6:17:00
RandomizedDelaySec=30min
Persistent=true

[Install]
WantedBy=timers.target
EOF
}

renew_timer_enable() {
  write_renew_units
  systemctl daemon-reload
  if ! systemctl enable --now o-ui-cert-renew.timer ||
     ! systemctl is-enabled --quiet o-ui-cert-renew.timer ||
     ! systemctl is-active --quiet o-ui-cert-renew.timer; then
    warn "自动续期定时器未正常启用，请检查 systemctl status o-ui-cert-renew.timer；证书不会保证自动续期" "automatic renewal timer is not enabled and active; inspect systemctl status o-ui-cert-renew.timer; automatic renewal is not guaranteed"
    return 1
  fi
}

renew_timer_disable() {
  if [ -f "$F_UNITS/o-ui-cert-renew.timer" ]; then
    systemctl disable --now o-ui-cert-renew.timer >/dev/null 2>&1 || true
    rm -f "$F_UNITS/o-ui-cert-renew.timer" "$F_UNITS/o-ui-cert-renew.service"
    systemctl daemon-reload
  fi
}

# 网页可修改监听地址；启动检查以systemd主进程的实际TCP监听为准。
panel_process_listening() {
  local pid
  pid=$(systemctl show o-ui -p MainPID --value 2>/dev/null) || return 1
  case $pid in ''|0|*[!0-9]*) return 1 ;; esac
  ss -H -lntp 2>/dev/null | awk -v pid="$pid" 'index($0, "pid=" pid ",") { found = 1 } END { exit !found }'
}

# Type=simple启动即active，再确认主进程监听并稳定存活；无ss时只看服务状态。
wait_active() {
  local i=0
  while [ $i -lt 20 ]; do
    if systemctl is-active --quiet o-ui && { panel_process_listening || { ! command -v ss >/dev/null 2>&1 && [ $i -ge 3 ]; }; }; then
      sleep 1
      if systemctl is-active --quiet o-ui && { panel_process_listening || ! command -v ss >/dev/null 2>&1; }; then return 0; fi
    fi
    sleep 1
    i=$((i + 1))
  done
  return 1
}

# ---------- 面板离线设置（以服务用户执行，面板须已停止） ----------

panel_init_admin() {
  printf '%s\n' "$2" | as_service "$F_INSTALL/bin/o-ui" -init-panel-admin -state "$F_DATA/panel" -username "$1"
}

panel_enable_https() {
  local listen=0.0.0.0:$OUI_PORT origin=https://$OUI_HOST:$OUI_PORT
  if is_ipv6 "$OUI_HOST"; then listen=[::]:$OUI_PORT; origin=https://[$OUI_HOST]:$OUI_PORT; fi
  as_service "$F_INSTALL/bin/o-ui" -enable-panel-https -state "$F_DATA/panel" \
    -panel-cert "$F_TLS/fullchain.pem" -panel-key "$F_TLS/key.pem" \
    -panel-listen "$listen" -panel-origin "$origin" -confirm-https "$HTTPS_CONFIRM"
}

panel_local_only() {
  as_service "$F_INSTALL/bin/o-ui" -recover-panel-access -state "$F_DATA/panel" \
    -listen "127.0.0.1:$OUI_PORT" -confirm-recovery "$RECOVERY_CONFIRM"
}

panel_set_path() {
  as_service "$F_INSTALL/bin/o-ui" -set-panel-path "$OUI_PATH" -state "$F_DATA/panel"
}

panel_reset_admin() {
  printf '%s\n' "$2" | as_service "$F_INSTALL/bin/o-ui" -reset-panel-admin -state "$F_DATA/panel" -username "$1"
}

# 面板运行中也可只读查询，输出单行JSON
panel_show() {
  as_service "$F_INSTALL/bin/o-ui" -show-panel -state "$F_DATA/panel"
}

# 取面板自身输出的扁平JSON中的字符串或布尔字段
json_field() {
  printf '%s' "$1" | sed -n -e "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p" -e "s/.*\"$2\":\(true\)[,}].*/\1/p" -e "s/.*\"$2\":\(false\)[,}].*/\1/p" | head -n 1
}

panel_url() {
  local path=${OUI_PATH:-/}
  if [ "$OUI_MODE" = local ]; then
    printf 'http://127.0.0.1:%s%s' "$OUI_PORT" "$path"
  elif is_ipv6 "$OUI_HOST"; then
    printf 'https://[%s]:%s%s' "$OUI_HOST" "$OUI_PORT" "$path"
  else
    printf 'https://%s:%s%s' "$OUI_HOST" "$OUI_PORT" "$path"
  fi
}

# ---------- 证书 ----------

acme() {
  "$F_ACME/acme.sh" --home "$F_ACME" --config-home "$F_ACME/data" --cert-home "$F_ACME/certs" "$@"
}

http_service_allowed() {
  [[ $1 =~ ^[A-Za-z0-9][A-Za-z0-9_.@-]*\.service$ ]] || return 1
  case $1 in ssh.service|sshd.service|ssh@*.service|sshd@*.service|docker.service|containerd.service|dbus*.service|systemd-*.service|NetworkManager*.service|network*.service|networking.service|o-ui*.service) return 1 ;; esac
}

# 忽略调用者的远程Docker context/TLS设置，始终使用本机rootful守护进程。
http_docker() {
  DOCKER_CONTEXT= DOCKER_TLS_VERIFY= DOCKER_CERT_PATH= docker --host unix:///var/run/docker.sock "$@"
}

# 固定实际unit名称或容器完整ID；不执行用户输入的命令，不以容器名称保存长期授权。
http_target_resolve() {
  local target=$1 id triggered
  case $target in
    docker:*)
      target=${target#docker:}
      [[ $target =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || return 1
      command -v docker >/dev/null 2>&1 || return 1
      id=$(http_docker inspect --format '{{.Id}}' "$target" 2>/dev/null) || return 1
      [[ $id =~ ^[0-9a-f]{64}$ ]] || return 1
      printf 'docker:%s' "$id" ;;
    *)
      http_service_allowed "$target" || return 1
      id=$(systemctl show "$target" -p Id --value) || return 1
      http_service_allowed "$id" || return 1
      [ "$(systemctl show "$id" -p LoadState --value)" = loaded ] || return 1
      triggered=$(systemctl show "$id" -p TriggeredBy --value) || return 1
      [ -z "$triggered" ] || return 1
      printf '%s' "$id" ;;
  esac
}

http_target_active() {
  case $1 in
    docker:*) [ "$(http_docker inspect --format '{{.State.Running}}' "${1#docker:}" 2>/dev/null)" = true ] ;;
    *) systemctl is-active --quiet "$1" ;;
  esac
}

http_target_action() {
  case $2 in docker:*) http_docker "$1" "${2#docker:}" >/dev/null ;; *) systemctl "$1" "$2" ;; esac
}

# 自动发现只用于给出建议；无法匹配时，交互流程允许用户明确指定临停目标。
http_listener_service() {
  local listeners line pids pid unit found='' group
  command -v ss >/dev/null 2>&1 || return 1
  listeners=$(ss -H -lntp 'sport = :80' 2>/dev/null) || return 1
  [ -n "$listeners" ] || return 0
  while IFS= read -r line; do
    pids=$(printf '%s\n' "$line" | grep -oE 'pid=[0-9]+' | cut -d= -f2) || return 1
    [ -n "$pids" ] || return 1
    for pid in $pids; do
      unit=$(awk -F: '{n=split($3,a,"/"); for(i=n;i>0;i--) if(a[i] ~ /\.service$/) {print a[i]; break}}' "$F_PROC/$pid/cgroup" 2>/dev/null | sort -u) || return 1
      unit=$(http_target_resolve "$unit") || return 1
      group=$(systemctl show "$unit" -p ControlGroup --value) || return 1
      [ -n "$group" ] && [ "$group" != / ] || return 1
      awk -F: -v g="$group" '$3 == g || index($3,g"/") == 1 {found=1} END {exit !found}' "$F_PROC/$pid/cgroup" || return 1
      [ -z "$found" ] || [ "$found" = "$unit" ] || return 1
      found=$unit
    done
  done <<<"$listeners"
  http_target_active "$found" || return 1
  printf '%s' "$found"
}

http_listener_container() {
  local ids id ports found=''
  command -v docker >/dev/null 2>&1 || return 1
  ids=$(http_docker ps --no-trunc --format '{{.ID}}' 2>/dev/null) || return 1
  for id in $ids; do
    [[ $id =~ ^[0-9a-f]{64}$ ]] || return 1
    ports=$(http_docker inspect --format '{{range .NetworkSettings.Ports}}{{range .}}{{println .HostPort}}{{end}}{{end}}' "$id" 2>/dev/null) || return 1
    if printf '%s\n' "$ports" | grep -qx 80; then
      [ -z "$found" ] || return 1
      found=docker:$id
    fi
  done
  [ -n "$found" ] && http_target_active "$found" || return 1
  printf '%s' "$found"
}

http_resume() {
  local unit
  [ -e "$F_ACME/http-paused" ] || return 0
  [ -f "$F_ACME/http-paused" ] && [ ! -L "$F_ACME/http-paused" ] || return 1
  unit=$(cat "$F_ACME/http-paused")
  [ "$(http_target_resolve "$unit")" = "$unit" ] || {
    warn "临停目标 $unit 已不存在或不允许恢复；记录保留，请立即人工检查" "paused target $unit is missing or cannot be restored; record retained, inspect immediately"; return 1; }
  if http_target_action start "$unit" && http_target_active "$unit"; then
    rm -f "$F_ACME/http-paused" || return 1
    info "已恢复网站服务：$unit" "Website service restored: $unit"
  else
    warn "网站服务 $unit 恢复失败！请立即检查该服务或容器；恢复记录已保留" "FAILED to restore website service $unit! Inspect the service or container immediately; recovery record retained"
    return 1
  fi
}

cmd_cert_http_stop() {
  need_root
  [ "${O_UI_HTTP_GUARD:-}" = 1 ] || die "请通过o-ui证书命令运行，拒绝单独临停网站" "run through an o-ui certificate command; refusing an unguarded website stop"
  [ "${1-}" = http ] && [ "${O_UI_HTTP_CHALLENGE:-}" = http ] || return 0
  local unit=${O_UI_HTTP_SERVICE:-} detected i listeners
  listeners=$(ss -H -lntp 'sport = :80' 2>/dev/null) || return 1
  [ -n "$listeners" ] || return 0
  [ -n "$unit" ] && [ "$(http_target_resolve "$unit")" = "$unit" ] && http_target_active "$unit" || {
    warn "80端口被未授权的服务占用，不能自动停止；请重新配置证书或使用DNS验证" "port 80 is held by an unauthorized service; reconfigure the certificate or use DNS validation"; return 1; }
  if detected=$(http_listener_service) && [ -n "$detected" ] && [ "$detected" != "$unit" ]; then
    warn "80端口监听者已变为未授权的 ${detected}，请重新确认临停目标" "port 80 listener changed to unauthorized $detected; confirm a new pause target"; return 1
  fi
  # 先记录再停，即使stop部分失败，退出兜底仍会尝试恢复。
  (umask 077; printf '%s\n' "$unit" >"$F_ACME/http-paused") || return 1
  info "临时停止网站服务：${unit}（申请结束后恢复）" "Temporarily stopping website service: $unit (restored after validation)"
  http_target_action stop "$unit" || return 1
  for ((i=0; i<10; i++)); do
    listeners=$(ss -H -lntp 'sport = :80' 2>/dev/null) || return 1
    if [ -z "$listeners" ] && ! http_target_active "$unit"; then return 0; fi
    sleep 1
  done
  warn "80端口未成功释放，停止申请并恢复网站" "port 80 was not released; aborting issuance and restoring the website"
  return 1
}

cmd_cert_http_resume() {
  need_root
  [ -d "$F_ACME" ] || return 0
  if [ "${O_UI_HTTP_GUARD:-}" = 1 ]; then http_resume; return; fi
  # systemd清理或管理员救援也须排除仍在进行的签发。
  (need_cmd flock; exec 9>"$F_ACME/operation.lock"; flock -n 9 || exit 1; http_resume)
}

acme_guard_worker() {
  umask 077
  need_cmd flock setsid
  mkdir -p "$F_ACME"
  chmod 0700 "$F_ACME"
  exec 9>"$F_ACME/operation.lock"
  flock -n 9 || die "另一项证书操作正在运行，请稍后重试" "another certificate operation is running; retry later"
  http_resume || exit 1
  export O_UI_HTTP_GUARD=1 O_UI_HTTP_SERVICE="$OUI_HTTP_SERVICE" O_UI_HTTP_CHALLENGE="$OUI_CHALLENGE"
  local child='' rc i
  trap 'rc=$?; trap - EXIT; http_resume || rc=1; exit "$rc"' EXIT
  trap '
    trap "" HUP INT TERM
    if [ -n "$child" ]; then
      kill -TERM -- "-$child" 2>/dev/null || true
      for ((i=0; i<10; i++)); do kill -0 "$child" 2>/dev/null || break; sleep 1; done
      kill -KILL -- "-$child" 2>/dev/null || true
      wait "$child" 2>/dev/null || true
    fi
    exit 130
  ' HUP INT TERM
  setsid "$F_ACME/acme.sh" --home "$F_ACME" --config-home "$F_ACME/data" --cert-home "$F_ACME/certs" "$@" &
  child=$!
  if wait "$child"; then exit 0; else exit $?; fi
}

acme_guarded() {
  local worker rc
  acme_guard_worker "$@" &
  worker=$!
  trap 'trap "" HUP INT TERM; kill -TERM "$worker" 2>/dev/null || true; wait "$worker" 2>/dev/null || true; exit 130' HUP INT TERM
  if wait "$worker"; then rc=0; else rc=$?; fi
  trap - HUP INT TERM
  return "$rc"
}

# 安装固定版本acme.sh到独立目录，不影响系统已有acme.sh及其cron
acme_ensure() {
  local tgz src
  [ -x "$F_ACME/acme.sh" ] && return 0
  printf '%s' "$ACME_SH_SHA256" | grep -Eq '^[0-9a-f]{64}$' || die "此安装脚本未固定acme.sh摘要，不能自动下载" "this installer has no pinned acme.sh digest; refusing to download"
  ensure_tool openssl openssl || die "acme.sh需要openssl" "acme.sh requires openssl"
  [ -n "$WORK" ] || WORK=$(mktemp -d)
  tgz=$WORK/acme.sh-$ACME_SH_VERSION.tar.gz
  info "下载acme.sh $ACME_SH_VERSION（GitHub官方标签）" "Downloading acme.sh $ACME_SH_VERSION (official GitHub tag)"
  fetch "https://github.com/acmesh-official/acme.sh/archive/refs/tags/$ACME_SH_VERSION.tar.gz" "$tgz" || die "下载acme.sh失败" "failed to download acme.sh"
  [ "$(sha256_of "$tgz")" = "$ACME_SH_SHA256" ] || die "acme.sh摘要不符，已停止" "acme.sh digest mismatch; aborted"
  mkdir "$WORK/acme-src"
  tar -xzf "$tgz" -C "$WORK/acme-src" --no-same-owner
  src=$WORK/acme-src/acme.sh-$ACME_SH_VERSION
  [ -f "$src/acme.sh" ] || die "acme.sh包结构异常" "unexpected acme.sh archive layout"
  mkdir -p "$F_ACME"
  chmod 0700 "$F_ACME"
  (
    umask 077
    cd "$src"
    ./acme.sh --install --nocron --noprofile --home "$F_ACME" --config-home "$F_ACME/data" --cert-home "$F_ACME/certs"
  ) || die "acme.sh安装失败" "acme.sh installation failed"
}

cert_fix_perms() {
  local f
  mkdir -p "$F_TLS"
  chown "root:$SERVICE_USER" "$F_TLS"
  chmod 0750 "$F_TLS"
  for f in fullchain.pem key.pem; do
    [ -f "$F_TLS/$f" ] && [ ! -L "$F_TLS/$f" ] || return 1
    chown "root:$SERVICE_USER" "$F_TLS/$f"
    chmod 0640 "$F_TLS/$f"
  done
}

cert_pair_valid() {
  local cert=$1 key=$2 pub keypub
  [ -f "$cert" ] && [ ! -L "$cert" ] && [ -f "$key" ] && [ ! -L "$key" ] || return 1
  openssl x509 -in "$cert" -noout -checkend 0 >/dev/null 2>&1 || return 1
  pub=$(openssl x509 -in "$cert" -noout -pubkey 2>/dev/null) || return 1
  keypub=$(openssl pkey -in "$key" -passin pass: -pubout 2>/dev/null) || return 1
  [ -n "$pub" ] && [ "$pub" = "$keypub" ]
}

# ACME只写暂存区，校验成功后再替换在线文件；错误续期产物不会覆盖旧证书。
cert_promote() {
  local pending=$F_ACME/deploy backup
  CERT_PREVIOUS=
  cert_pair_valid "$pending/fullchain.pem" "$pending/key.pem" || {
    warn "新证书无效、已过期或私钥不配对，原在线证书保持不变" "new certificate is invalid, expired or does not match the key; live certificate unchanged"; return 1; }
  mkdir -p "$F_TLS" || return 1
  [ ! -L "$F_TLS" ] && [ ! -L "$F_TLS/fullchain.pem" ] && [ ! -L "$F_TLS/key.pem" ] || return 1
  if [ -f "$F_TLS/fullchain.pem" ] && [ -f "$F_TLS/key.pem" ]; then
    if cmp -s "$pending/fullchain.pem" "$F_TLS/fullchain.pem" && cmp -s "$pending/key.pem" "$F_TLS/key.pem"; then cert_fix_perms; return; fi
    backup=$(mktemp -d "$F_ACME/previous.XXXXXXXX") || return 1
    chmod 0700 "$backup" || return 1
    cp -p "$F_TLS/fullchain.pem" "$F_TLS/key.pem" "$backup/" || return 1
    CERT_PREVIOUS=$backup
  fi
  install -m 0640 "$pending/fullchain.pem" "$F_TLS/fullchain.pem.next" || return 1
  install -m 0640 "$pending/key.pem" "$F_TLS/key.pem.next" || return 1
  chown "root:$SERVICE_USER" "$F_TLS/fullchain.pem.next" "$F_TLS/key.pem.next" || return 1
  mv -f "$F_TLS/key.pem.next" "$F_TLS/key.pem" || return 1
  mv -f "$F_TLS/fullchain.pem.next" "$F_TLS/fullchain.pem" || return 1
  cert_fix_perms
}

cert_restore_previous() {
  [ -n "$CERT_PREVIOUS" ] || return 0
  cp -p "$CERT_PREVIOUS/fullchain.pem" "$CERT_PREVIOUS/key.pem" "$F_TLS/" && cert_fix_perms
}

# 证书名称（SAN中的域名与IP，逗号分隔）、到期时间；面板只按SAN校验名称
cert_names() {
  { openssl x509 -in "$1" -noout -text 2>/dev/null || true; } | awk '/Subject Alternative Name/ { getline; gsub(/ /, ""); gsub(/IPAddress:/, ""); gsub(/DNS:/, ""); print tolower($0); exit }'
}

cert_end() { { openssl x509 -in "$1" -noout -enddate 2>/dev/null || true; } | sed 's/^notAfter=//'; }

# 证书统一由ACME申请并自动续期；不扫描或导入外部证书。
CERT_EMAIL= CERT_CF_TOKEN= CERT_CF_ZONE= CERT_FORCE= CERT_PREVIOUS=
ask_access() {
  local n host unit target listeners
  info "面板证书：ACME / Let's Encrypt，自动申请并续期。" "Panel certificate: ACME / Let's Encrypt, automatically issued and renewed."
  OUI_MODE=acme OUI_RENEW=timer
  OUI_HTTP_SERVICE=
  while :; do
    ask host "面板域名或公网IP（证书将签发给它）" "Panel domain or public IP (the certificate is issued for it)" "$OUI_HOST"
    host=$(printf '%s' "$host" | tr 'A-Z' 'a-z')
    case $host in ''|*[!a-z0-9.:-]*) continue ;; esac
    if is_ip "$host" || printf '%s' "$host" | grep -Eq '^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,63}$'; then break; fi
  done
  OUI_HOST=$host
  if is_ip "$OUI_HOST"; then
    info "IP证书为Let's Encrypt短期证书（约6天），只能用HTTP-01（需80端口），约每3天续期并重启面板。" "IP certificates are Let's Encrypt short-lived (~6 days), HTTP-01 only (port 80), renewed about every 3 days with a panel restart."
    OUI_CHALLENGE=http
  else
    info "验证方式：" "Validation method:"
    info "  1) HTTP-01（申请与续期时需80端口空闲且公网可达）" "  1) HTTP-01 (port 80 must be free and reachable during issue and renewal)"
    info "  2) Cloudflare DNS API（不占用80端口，需API令牌）" "  2) Cloudflare DNS API (no port 80, needs an API token)"
    choose n 1 2
    OUI_CHALLENGE=http
    if [ "$n" = 2 ]; then OUI_CHALLENGE=cf; fi
  fi
  if [ "$OUI_CHALLENGE" = http ]; then
    if ! command -v ss >/dev/null 2>&1 || ! listeners=$(ss -H -lntp 'sport = :80' 2>/dev/null); then
      warn "无法检查80端口，请安装iproute2或改用DNS验证；未临停任何服务" "cannot inspect port 80; install iproute2 or use DNS validation; no service paused"
      ask_access
      return 0
    fi
    if ! unit=$(http_listener_service) && ! unit=$(http_listener_container); then
      warn "80端口已被占用，无法自动确定临停目标；可手动指定服务或Docker容器，确认后临停，不直接终止未知进程" "port 80 is occupied; specify a service or Docker container to pause after confirmation; unknown processes will not be killed"
      printf '%s\n' "$listeners"
      info "可用 docker ps 查看容器；例如 nginx.service 或 docker:容器名。多个监听者须先自行协调，脚本每次只临停一个目标。" "Use docker ps to find containers; enter e.g. nginx.service or docker:container-name. Coordinate multiple listeners separately; only one target is paused."
      ask target "临停目标（完整服务名或docker:容器名，留空返回验证方式选择）" "Pause target (full service name or docker:container-name; empty to choose validation again)" ""
      if [ -z "$target" ]; then ask_access; return 0; fi
      if ! unit=$(http_target_resolve "$target") || ! http_target_active "$unit"; then
        warn "临停目标无效或不允许：须为运行中的网站服务/容器，不允许SSH、网络、Docker守护进程、面板或socket激活服务" "invalid or prohibited pause target: use an active website service/container; SSH, networking, Docker daemon, panel and socket-activated services are prohibited"
        ask_access
        return 0
      fi
    fi
    if [ -n "$unit" ]; then
      warn "待临停目标：${unit}；临停整个服务或容器也会中断它承载的HTTPS网站及其他业务。申请失败或可捕获中断也会尝试恢复；强杀或断电不能保证恢复。不会修改网站配置、开机状态或防火墙" "Pause target: $unit; stopping the whole service/container also interrupts its HTTPS websites and other workloads. Restoration is attempted on failure or catchable interruption; SIGKILL or power loss cannot guarantee restoration. Website configuration, enablement and firewall are unchanged"
      if ask_yes "授权申请及今后真正续期时临停 ${unit}，并在结束后恢复？" "Authorize temporarily stopping $unit during issuance and future due renewals, then restoring it?" n; then
        OUI_HTTP_SERVICE=$unit
      else
        ask_access
        return 0
      fi
    fi
  fi
  if [ "$OUI_CHALLENGE" = cf ]; then
    while [ -z "$CERT_CF_TOKEN" ]; do
      ask_secret CERT_CF_TOKEN "Cloudflare API令牌（需Zone.DNS编辑权限，输入不显示）" "Cloudflare API token (Zone.DNS edit permission, hidden input)"
    done
    ask CERT_CF_ZONE "Cloudflare Zone ID（可留空，由令牌查找）" "Cloudflare Zone ID (optional, looked up with the token)" ""
    case $CERT_CF_ZONE in *[!A-Za-z0-9]*) die "Zone ID无效" "invalid Zone ID" ;; esac
  fi
  ask CERT_EMAIL "ACME账号邮箱（可留空）" "ACME account email (optional)" ""
  case $CERT_EMAIL in ''|*@*.*) ;; *) die "邮箱格式无效" "invalid email" ;; esac
  case $CERT_EMAIL in *[!A-Za-z0-9._%+@-]*) die "邮箱格式无效" "invalid email" ;; esac
  info "自动续期：每6小时检查，续期成功后重启面板加载证书，代理可能短暂中断。" "Automatic renewal: checked every 6 hours; successful renewal restarts the panel to load certificates and may briefly interrupt proxies."
}

# 申请证书并导出到/etc/o-ui/tls；失败返回非零
obtain_cert() {
  if [ "$OUI_CHALLENGE" = http ]; then
    command -v socat >/dev/null 2>&1 || command -v python3 >/dev/null 2>&1 || ensure_tool socat socat || {
      warn "HTTP-01需要socat或python3" "HTTP-01 needs socat or python3"; return 1; }
  fi
  acme_ensure
  ensure_tool flock util-linux && ensure_tool setsid util-linux || return 1
  local rc args
  args=(--issue --server letsencrypt -d "$OUI_HOST" --keylength ec-256)
  [ -z "$CERT_FORCE" ] || args+=(--force)
  [ -z "$CERT_EMAIL" ] || args+=(--accountemail "$CERT_EMAIL")
  if [ "$OUI_CHALLENGE" = cf ]; then args+=(--dns dns_cf); else args+=(--standalone); fi
  is_ipv6 "$OUI_HOST" && args+=(--listen-v6)
  is_ip "$OUI_HOST" && args+=(--cert-profile shortlived --days 3)
  args+=(--pre-hook "$F_CMD cert-http-stop $OUI_CHALLENGE" --post-hook "$F_CMD cert-http-resume")
  info "正在申请证书（$OUI_HOST）…" "Requesting certificate ($OUI_HOST)…"
  set +e
  if [ "$OUI_CHALLENGE" = cf ]; then
    CF_Token="$CERT_CF_TOKEN" CF_Zone_ID="$CERT_CF_ZONE" acme_guarded "${args[@]}"
  else
    acme_guarded "${args[@]}"
  fi
  rc=$?
  set -e
  # acme.sh返回2表示证书仍有效、无需重新签发
  [ $rc -eq 0 ] || [ $rc -eq 2 ] || { warn "证书申请失败（acme.sh退出码$rc）" "certificate request failed (acme.sh exit $rc)"; return 1; }
  mkdir -p "$F_ACME/deploy"
  chmod 0700 "$F_ACME" "$F_ACME/deploy"
  O_UI_CERT_SETUP=1 acme --install-cert -d "$OUI_HOST" --ecc --key-file "$F_ACME/deploy/key.pem" --fullchain-file "$F_ACME/deploy/fullchain.pem" \
    --reloadcmd "$P_CMD cert-reload --pending" || { warn "证书导出失败" "certificate install failed"; return 1; }
  cert_pair_valid "$F_ACME/deploy/fullchain.pem" "$F_ACME/deploy/key.pem"
}

# 停用旧ACME条目（换域名/换方式时），避免定时器继续续期
acme_forget() {
  [ -n "$1" ] && [ -x "$F_ACME/acme.sh" ] || return 0
  acme --remove -d "$1" --ecc >/dev/null 2>&1 || true
}

# ---------- BBR ----------

bbr_current() { printf '%s/%s' "$(cat "$F_PROC/sys/net/core/default_qdisc" 2>/dev/null)" "$(cat "$F_PROC/sys/net/ipv4/tcp_congestion_control" 2>/dev/null)"; }

bbr_supported() {
  local v major minor
  v=$(uname -r)
  major=${v%%.*}; minor=${v#*.}; minor=${minor%%[!0-9]*}
  case $major$minor in *[!0-9]*|'') return 1 ;; esac
  [ "$major" -gt 4 ] || { [ "$major" -eq 4 ] && [ "$minor" -ge 9 ]; } || return 1
  grep -qw bbr "$F_PROC/sys/net/ipv4/tcp_available_congestion_control" 2>/dev/null && return 0
  command -v modprobe >/dev/null 2>&1 && modprobe tcp_bbr 2>/dev/null || return 1
  grep -qw bbr "$F_PROC/sys/net/ipv4/tcp_available_congestion_control" 2>/dev/null || return 1
  OUI_BBR_MODULE=1
}

bbr_offer() {
  local cur
  cur=$(bbr_current)
  if [ "$cur" = fq/bbr ]; then info "BBR已开启（fq/bbr），不做改动。" "BBR is already on (fq/bbr); no change."; return 1; fi
  if ! bbr_supported; then info "当前内核不支持BBR（需4.9+且含tcp_bbr），跳过。" "The kernel does not support BBR (needs 4.9+ with tcp_bbr); skipped."; return 1; fi
  info "BBR：当前 队列/拥塞控制 = $cur；开启后写入 ${F_SYSCTL#$R}（default_qdisc=fq、tcp_congestion_control=bbr），可用 o-ui bbr off 回退。" \
    "BBR: current qdisc/congestion = $cur; enabling writes ${F_SYSCTL#$R} (default_qdisc=fq, tcp_congestion_control=bbr); revert with o-ui bbr off."
  ask_yes "是否开启BBR？" "Enable BBR?" n
}

bbr_enable() {
  local cur other
  cur=$(bbr_current)
  [ "$cur" != fq/bbr ] || { info "BBR已开启。" "BBR is already on."; return 0; }
  bbr_supported || { warn "内核不支持BBR" "kernel does not support BBR"; return 1; }
  if [ -z "$OUI_BBR_PREV_CC" ]; then OUI_BBR_PREV_QDISC=${cur%%/*}; OUI_BBR_PREV_CC=${cur#*/}; fi
  mkdir -p "$(dirname "$F_SYSCTL")"
  printf '%s\n' '# o-ui: BBR，删除本文件并运行 o-ui bbr off 可回退' 'net.core.default_qdisc=fq' 'net.ipv4.tcp_congestion_control=bbr' >"$F_SYSCTL"
  if [ "$OUI_BBR_MODULE" = 1 ]; then mkdir -p "$(dirname "$F_MODULES")"; printf 'tcp_bbr\n' >"$F_MODULES"; fi
  if ! sysctl -p "$F_SYSCTL" >/dev/null || [ "$(bbr_current)" != fq/bbr ]; then
    rm -f "$F_SYSCTL" "$F_MODULES"
    sysctl -w "net.core.default_qdisc=$OUI_BBR_PREV_QDISC" "net.ipv4.tcp_congestion_control=$OUI_BBR_PREV_CC" >/dev/null 2>&1 || true
    OUI_BBR_PREV_QDISC= OUI_BBR_PREV_CC= OUI_BBR_MODULE=
    conf_save
    warn "BBR设置未生效（容器或内核限制），已撤销" "BBR could not be applied (container or kernel limits); reverted"
    return 1
  fi
  conf_save
  other=$(grep -lsE '^[[:space:]]*net\.(core\.default_qdisc|ipv4\.tcp_congestion_control)' "$R/etc/sysctl.conf" "$R"/etc/sysctl.d/*.conf 2>/dev/null | grep -v "/99-o-ui-bbr.conf$" || true)
  [ -z "$other" ] || warn "以下文件也设置了同一参数，可能在重启后覆盖：$other" "these files set the same keys and may override after reboot: $other"
  info "BBR已开启：$(bbr_current)（原值 $OUI_BBR_PREV_QDISC/$OUI_BBR_PREV_CC）。fq对已存在的网卡队列在重启或重建后生效。" \
    "BBR is on: $(bbr_current) (was $OUI_BBR_PREV_QDISC/$OUI_BBR_PREV_CC). fq applies to existing interfaces after reboot or re-creation."
}

bbr_disable() {
  if [ ! -f "$F_SYSCTL" ]; then info "未找到o-ui写入的BBR设置，不做改动。" "No o-ui BBR setting found; no change."; return 0; fi
  rm -f "$F_SYSCTL" "$F_MODULES"
  if [ -n "$OUI_BBR_PREV_CC" ]; then
    sysctl -w "net.core.default_qdisc=$OUI_BBR_PREV_QDISC" "net.ipv4.tcp_congestion_control=$OUI_BBR_PREV_CC" >/dev/null ||
      warn "恢复原值失败，重启后按系统默认" "failed to restore previous values; system defaults apply after reboot"
    info "已回退BBR设置：当前 $(bbr_current)。" "BBR setting reverted: now $(bbr_current)."
  else
    info "已删除o-ui的BBR持久化设置；未记录原值，当前值 $(bbr_current) 在重启后恢复系统默认。" "Removed the o-ui BBR setting; no previous values were recorded, current $(bbr_current) returns to system defaults after reboot."
  fi
  OUI_BBR_PREV_QDISC= OUI_BBR_PREV_CC= OUI_BBR_MODULE=
  conf_save
}

# ---------- 防火墙：既有规则不接管，全新环境经确认后配置UFW ----------

fw_existing_rules() {
  local rules
  if command -v ufw >/dev/null 2>&1; then
    rules=$(LC_ALL=C ufw show added 2>/dev/null) || return 2
    if printf '%s\n' "$rules" | grep -Eq '^ufw '; then return 0; fi
  fi
  # 任意既有规则都保守保留，包括Docker/NAT；无法读取时不猜测是空机器。
  command -v nft >/dev/null 2>&1 || return 2
  rules=$(nft list ruleset 2>/dev/null) || return 2
  [ -n "${rules//[[:space:]]/}" ] && return 0
  if command -v iptables-save >/dev/null 2>&1; then
    rules=$(iptables-save 2>/dev/null) || return 2
    if printf '%s\n' "$rules" | grep -Eq '^(-A |:[^ ]+ (DROP|REJECT))'; then return 0; fi
  fi
  if command -v ip6tables-save >/dev/null 2>&1; then
    rules=$(ip6tables-save 2>/dev/null) || return 2
    if printf '%s\n' "$rules" | grep -Eq '^(-A |:[^ ]+ (DROP|REJECT))'; then return 0; fi
  fi
  return 1
}

fw_install_offer() {
  local ssh_port='' ssh_client ssh_client_port ssh_server rules_status=0 backup
  info "可安装UFW防火墙及自动放行组件。现有网站等服务端口不会自动放行，启用后仅保留明确允许的入站。" "You can install UFW and automatic port-management components. Existing website and other service ports are not opened automatically; only explicitly allowed inbound traffic will remain reachable."
  ask_yes "是否安装并启用整套防火墙？将先放行SSH、面板及HTTP-01续期所需端口，再默认拒绝其他入站" "Install and enable the firewall stack? SSH, panel and HTTP-01 renewal ports will be allowed first; other inbound traffic will be denied by default" n || return 0
  if fw_existing_rules; then rules_status=0; else rules_status=$?; fi
  if [ "$rules_status" = 0 ]; then
    warn "检测到既有nftables/iptables规则，保留原防火墙，不安装或启用另一套；请在原规则中放行面板和节点端口" "existing nftables/iptables rules detected; keeping them without installing or enabling another firewall; allow panel and node ports in the existing rules"
    return 0
  fi
  # 不静默假定SSH是22；SSH连接环境缺失时要求用户填写。
  if [ -n "${SSH_CONNECTION:-}" ]; then
    read -r ssh_client ssh_client_port ssh_server ssh_port <<<"$SSH_CONNECTION"
  fi
  if [ -z "$ssh_port" ]; then
    ask ssh_port "当前SSH服务端口（必须准确填写，避免锁定远程访问）" "Current SSH server port (must be accurate to avoid locking out remote access)"
  fi
  case $ssh_port in ''|*[!0-9]*|0*) warn "SSH端口无效，未启用防火墙" "invalid SSH port; firewall not enabled"; return 0 ;; esac
  if [ ${#ssh_port} -gt 5 ] || [ "$ssh_port" -lt 1 ] || [ "$ssh_port" -gt 65535 ]; then warn "SSH端口无效，未启用防火墙" "invalid SSH port; firewall not enabled"; return 0; fi
  info "将保留当前SSH连接使用的 $ssh_port/tcp、面板 $OUI_PORT/tcp；HTTP-01证书还会保留80/tcp。其他SSH端口和业务需自行添加规则；云安全组仍需另行放行。" "Will keep the current SSH connection's $ssh_port/tcp and panel $OUI_PORT/tcp reachable; HTTP-01 also keeps 80/tcp. Other SSH ports and services need separate rules; cloud security groups must be configured separately."
  # UFW部署前后均检查；安装软件包可能自动创建规则，不能覆盖并发管理操作。
  pkg_install ufw sudo nftables python3 iptables || { warn "防火墙组件安装失败，未启用防火墙" "firewall component installation failed; firewall not enabled"; return 0; }
  need_cmd ufw nft python3 sudo visudo iptables ip6tables
  if ufw status 2>/dev/null | grep -i '^status: active' >/dev/null; then warn "UFW已被其他进程启用，不更改默认策略" "UFW was enabled by another process; default policies are unchanged"; return 0; fi
  if fw_existing_rules; then rules_status=0; else rules_status=$?; fi
  if [ "$rules_status" != 1 ]; then warn "现有规则非空或无法完整检查，未启用UFW，保留现场" "existing rules are nonempty or could not be inspected completely; UFW not enabled and existing state kept"; return 0; fi
  backup=$(mktemp -d "$F_ETC/firewall-backup.XXXXXXXX")
  chmod 0700 "$backup"
  [ ! -d "$R/etc/ufw" ] || cp -a "$R/etc/ufw" "$backup/ufw"
  [ ! -f "$R/etc/default/ufw" ] || cp -p "$R/etc/default/ufw" "$backup/default-ufw"
  if ! grep -Eq '^IPV6=yes([[:space:]]|$)' "$R/etc/default/ufw"; then
    warn "UFW未启用IPv6管理，保留原配置且不启用，避免IPv6绕过入站策略；备份：$backup" "UFW IPv6 management is not enabled; preserving configuration without enabling UFW to prevent IPv6 policy bypass; backup: $backup"; return 0
  fi
  # 未启用前写允许规则；任一步失败都不执行enable。
  if ! ufw allow "$ssh_port/tcp" comment 'o-ui SSH safety' >/dev/null ||
     ! ufw allow "$OUI_PORT/tcp" comment 'o-ui panel' >/dev/null; then
    warn "安全端口规则写入失败，未启用UFW；备份：$backup" "safety allow rules failed; UFW not enabled; backup: $backup"; return 0
  fi
  if [ "$OUI_CHALLENGE" = http ] && ! ufw allow 80/tcp comment 'o-ui ACME HTTP-01' >/dev/null; then
    warn "HTTP-01端口规则写入失败，未启用UFW；备份：$backup" "HTTP-01 allow rule failed; UFW not enabled; backup: $backup"; return 0
  fi
  if ! ufw default deny incoming >/dev/null || ! ufw default allow outgoing >/dev/null; then
    warn "默认策略设置失败，未启用UFW；备份：$backup" "default policy setup failed; UFW not enabled; backup: $backup"; return 0
  fi
  if ! ufw --force enable >/dev/null; then
    warn "UFW启用失败，请检查 ufw status；备份：$backup" "UFW enable failed; inspect ufw status; backup: $backup"; return 0
  fi
  if ! LC_ALL=C ufw status | grep -qi '^status: active' || ! systemctl enable ufw >/dev/null || ! systemctl is-enabled --quiet ufw; then
    warn "UFW运行或开机自启核验失败，请检查 ufw status 和 systemctl status ufw；备份：$backup" "UFW active or boot-persistence verification failed; inspect ufw status and systemctl status ufw; backup: $backup"; return 0
  fi
  OUI_FW=ufw
  deploy_firewall_access
  info "UFW已启用并持久化；新建节点会自动放行，原配置备份：$backup" "UFW enabled persistently; new node ports are managed automatically; original config backup: $backup"
}

fw_offer() {
  if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -i '^status: active' >/dev/null; then
    if ask_yes "检测到UFW已启用，是否仅放行面板端口 $OUI_PORT/tcp？" "UFW is active; allow only the panel port $OUI_PORT/tcp?" y; then
      ufw allow "$OUI_PORT/tcp" comment 'o-ui panel' >/dev/null && OUI_FW=ufw
    fi
  elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    if ask_yes "检测到firewalld正在运行，是否仅放行面板端口 $OUI_PORT/tcp？" "firewalld is running; allow only the panel port $OUI_PORT/tcp?" y; then
      firewall-cmd --add-port="$OUI_PORT/tcp" >/dev/null && firewall-cmd --permanent --add-port="$OUI_PORT/tcp" >/dev/null && OUI_FW=firewalld
    fi
  else
    if [ "${1-}" = install ]; then fw_install_offer; fi
    if [ -z "$OUI_FW" ]; then info "未启用新的防火墙；如有云安全组或其他防火墙，请自行放行TCP $OUI_PORT。" "No new firewall was enabled; open TCP $OUI_PORT in any cloud security group or existing firewall yourself."; fi
  fi
  if [ "$OUI_CHALLENGE" = http ]; then
    info "HTTP-01申请与续期时需公网可访问80/tcp；本脚本不改80端口规则。" "HTTP-01 needs public access to 80/tcp during issue and renewal; this script does not change port 80 rules."
  fi
}

fw_remove() {
  local port=${1:-$OUI_PORT}
  case $OUI_FW in
    ufw) ufw delete allow "$port/tcp" >/dev/null || true ;;
    firewalld) firewall-cmd --remove-port="$port/tcp" >/dev/null 2>&1 || true; firewall-cmd --permanent --remove-port="$port/tcp" >/dev/null || true ;;
  esac
  OUI_FW=
}

# ---------- 网页升级助手（root拥有，面板经sudo调用，不接受参数） ----------

# 参照OUI的网页更新：在独立的systemd临时服务o-ui-update里运行已安装的“o-ui update”（升级到GitHub最新正式版），
# 面板停止重启时不受影响；固定服务名防止并发；结果写入root拥有的状态目录，面板只读。
write_update_helper() {
  local path=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin tmp
  [ -z "$R" ] || path=$PATH
  mkdir -p "$(dirname "$F_UPDATE")"
  tmp=$F_UPDATE.tmp
  cat >"$tmp" <<EOF
#!/bin/sh
# o-ui网页升级助手：只供o-ui服务经sudo无参数调用，升级到GitHub最新正式版；下载、摘要校验与失败回退由o-ui update完成。
set -eu
PATH='$path'
export PATH
SELF='$F_UPDATE'
CMD='$F_CMD'
STATE='$F_UPDATESTATE'
VERSION_FILE='$F_INSTALL/VERSION'
EOF
  cat >>"$tmp" <<'EOF'
usage() { echo "用法：o-ui-update（升级到GitHub最新正式版，不接受参数） / usage: o-ui-update (upgrade to the latest GitHub release; no arguments)" >&2; exit 2; }
status() { umask 022; mkdir -p "$STATE"; printf '%s
' "$1" >"$STATE/status.json.tmp"; mv "$STATE/status.json.tmp" "$STATE/status.json"; }
if [ "${1-}" = --run ]; then
  [ $# -eq 1 ] || usage
  from=$(tr -cd 'A-Za-z0-9.-' <"$VERSION_FILE" 2>/dev/null || true)
  status "{\"state\":\"running\",\"from\":\"$from\",\"to\":\"\",\"code\":0,\"at\":$(date +%s)}"
  code=0
  "$CMD" update </dev/null >"$STATE/output.log" 2>&1 || code=$?
  to=$(tr -cd 'A-Za-z0-9.-' <"$VERSION_FILE" 2>/dev/null || true)
  state=done
  [ "$code" -eq 0 ] || state=failed
  status "{\"state\":\"$state\",\"from\":\"$from\",\"to\":\"$to\",\"code\":$code,\"at\":$(date +%s)}"
  exit "$code"
fi
[ $# -eq 0 ] || usage
command -v systemd-run >/dev/null 2>&1 || { echo "需要systemd-run，请在服务器上运行 o-ui update / systemd-run is required; run o-ui update on the server"; exit 3; }
if systemctl is-active --quiet o-ui-update 2>/dev/null; then echo "升级已在进行 / an update is already running"; exit 4; fi
systemd-run --unit=o-ui-update --collect --quiet --description='o-ui web update' "$SELF" --run
echo "已开始升级 / update started"
EOF
  chown root:root "$tmp"
  chmod 0755 "$tmp"
  mv "$tmp" "$F_UPDATE"
}

# ---------- 入站端口防火墙助手（root拥有，面板经sudo调用） ----------

write_firewall_helper() {
  local path=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin tmp
  # 测试根目录下才沿用当前PATH以便命令桩生效；正式安装固定系统PATH，助手不读取任何环境路径
  [ -z "$R" ] || path=$PATH
  mkdir -p "$(dirname "$F_HELPER")"
  tmp=$F_HELPER.tmp
  cat >"$tmp" <<EOF
#!/bin/sh
# o-ui防火墙助手：只供o-ui服务经sudo调用，放行或撤销单个入站端口；只删除自己记录过的规则。
set -eu
PATH='$path'
export PATH
umask 077
RULES='$F_FWRULES'
NFT_CONFIG='$R/etc/nftables.d/host_hardening.nft'
EOF
  cat >>"$tmp" <<'EOF'
usage() { echo "用法：o-ui-firewall allow|delete <1024-65535> tcp|udp；ban|unban <IP> <1024-65535> tcp|udp；flush；redirect add|delete <起始-结束> <端口>；redirect flush / usage: o-ui-firewall allow|delete <1024-65535> tcp|udp; ban|unban <IP> <1024-65535> tcp|udp; flush; redirect add|delete <start-end> <port>; redirect flush" >&2; exit 2; }
valid_port() {
  case $1 in ''|*[!0-9]*|0*) return 1 ;; esac
  [ ${#1} -le 5 ] && [ "$1" -ge 1024 ] && [ "$1" -le 65535 ]
}
# IP上限：在独立链o-ui-iplimit里拒绝某个源IP访问某个入站端口（挂在INPUT最前）；
# 规则只在内存，重启后清空，由面板按记录重建；flush只清空本链，不碰其他防火墙规则。
CHAIN=o-ui-iplimit
if [ "${1-}" = flush ]; then
  [ $# -eq 1 ] || usage
  for table in iptables ip6tables; do
    if command -v "$table" >/dev/null 2>&1; then "$table" -w -F "$CHAIN" 2>/dev/null || true; fi
  done
  echo "已清空IP上限封禁 / IP limit bans cleared"
  exit 0
fi
if [ "${1-}" = ban ] || [ "${1-}" = unban ]; then
  [ $# -eq 4 ] || usage
  action=$1 ip=$2 port=$3 proto=$4
  valid_port "$port" || usage
  case $proto in tcp|udp) ;; *) usage ;; esac
  case $ip in
    *:*)
      case $ip in *[!0-9A-Fa-f:]*) usage ;; esac
      [ ${#ip} -le 39 ] || usage
      table=ip6tables ;;
    *)
      printf '%s\n' "$ip" | awk -F. 'NF != 4 { exit 1 } { for (i = 1; i <= 4; i++) if ($i !~ /^[0-9]+$/ || length($i) > 3 || $i > 255) exit 1 }' || usage
      table=iptables ;;
  esac
  command -v "$table" >/dev/null 2>&1 || { echo "未找到$table，未封禁 / $table not found; nothing changed"; exit 3; }
  if [ "$proto" = tcp ]; then
    set -- -s "$ip" -p tcp --dport "$port" -j REJECT --reject-with tcp-reset
  else
    set -- -s "$ip" -p udp --dport "$port" -j DROP
  fi
  if [ "$action" = ban ]; then
    "$table" -w -N "$CHAIN" 2>/dev/null || true
    "$table" -w -C INPUT -j "$CHAIN" 2>/dev/null || "$table" -w -I INPUT 1 -j "$CHAIN" || { echo "挂接$CHAIN失败 / failed to attach $CHAIN"; exit 1; }
    "$table" -w -C "$CHAIN" "$@" 2>/dev/null || "$table" -w -A "$CHAIN" "$@" || { echo "封禁$ip失败 / failed to ban $ip"; exit 1; }
    echo "已封禁 $ip → $port/$proto / banned $ip on $port/$proto"
  else
    while "$table" -w -D "$CHAIN" "$@" 2>/dev/null; do :; done
    echo "已解封 $ip → $port/$proto / unbanned $ip on $port/$proto"
  fi
  exit 0
fi
# HY2端口跳跃：在nat表独立链o-ui-hop里把一段UDP端口转发到监听端口（挂在PREROUTING）；
# 规则只在内存，重启后清空，由面板按入站记录重建；flush只清空本链。
HOP=o-ui-hop
if [ "${1-}" = redirect ]; then
  if [ "${2-}" = flush ] && [ $# -eq 2 ]; then
    for table in iptables ip6tables; do
      if command -v "$table" >/dev/null 2>&1; then "$table" -w -t nat -F "$HOP" 2>/dev/null || true; fi
    done
    echo "已清空端口跳跃转发 / port hopping forwards cleared"
    exit 0
  fi
  [ $# -eq 4 ] || usage
  action=$2 range=$3 port=$4
  case $action in add|delete) ;; *) usage ;; esac
  start=${range%-*} end=${range#*-}
  [ "$range" = "$start-$end" ] || usage
  valid_port "$start" && valid_port "$end" && valid_port "$port" || usage
  [ "$start" -lt "$end" ] || usage
  if [ "$port" -ge "$start" ] && [ "$port" -le "$end" ]; then usage; fi
  set -- -p udp --dport "$start:$end" -j REDIRECT --to-ports "$port"
  done=''
  for table in iptables ip6tables; do
    command -v "$table" >/dev/null 2>&1 || continue
    if [ "$action" = add ]; then
      "$table" -w -t nat -N "$HOP" 2>/dev/null || true
      { "$table" -w -t nat -C PREROUTING -j "$HOP" 2>/dev/null || "$table" -w -t nat -A PREROUTING -j "$HOP" 2>/dev/null; } &&
        { "$table" -w -t nat -C "$HOP" "$@" 2>/dev/null || "$table" -w -t nat -A "$HOP" "$@" 2>/dev/null; } && done="$done $table"
    else
      while "$table" -w -t nat -D "$HOP" "$@" 2>/dev/null; do :; done
    fi
  done
  if [ "$action" = delete ]; then
    echo "已撤销端口跳跃 $range/udp → $port / removed port hopping $range/udp → $port"
    exit 0
  fi
  case $done in
    *iptables*) ;;
    *) echo "未能设置端口跳跃转发（需要iptables的nat表） / could not set up port hopping (needs the iptables nat table)"; exit 3 ;;
  esac
  case $done in
    *ip6tables*) echo "已设置端口跳跃 $range/udp → $port / port hopping $range/udp → $port set" ;;
    *) echo "已设置端口跳跃 $range/udp → $port（仅IPv4） / port hopping $range/udp → $port set (IPv4 only)" ;;
  esac
  exit 0
fi
[ $# -eq 3 ] || usage
action=$1 port=$2 proto=$3
case $action in allow|delete) ;; *) usage ;; esac
valid_port "$port" || usage
case $proto in tcp|udp) ;; *) usage ;; esac
rule=$port/$proto
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -i '^status: active' >/dev/null; then
  backend=ufw
elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
  backend=firewalld
else
  backend=''
fi
# 原生nftables规则可能与UFW/firewalld并存；必须检查额外的INPUT拦截层。
if command -v nft >/dev/null 2>&1; then
  command -v python3 >/dev/null 2>&1 || { echo "检测到nftables，需要python3检查并持久化端口规则，请手动放行 / nftables detected; python3 is required, allow the port manually"; exit 3; }
  nft_status=0
  python3 -I - "$action" "$port" "$proto" "$RULES" "$NFT_CONFIG" "$backend" <<'PY' || nft_status=$?
import fcntl
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import time

action, port, proto, records_path, config_path, backend = sys.argv[1:]
port = int(port)
records, config = Path(records_path), Path(config_path)
marker = f"o-ui inbound {port}/{proto}"
record = f"nft {port}/{proto}\n"
line = f'    {proto} dport {port} counter accept comment "{marker}"\n'

def nft(*args, data=None):
    return subprocess.run(["nft", *args], input=data, text=True, capture_output=True, check=True, timeout=3).stdout

def write_atomic(path, data):
    info = path.stat() if path.exists() else None
    mode = stat.S_IMODE(info.st_mode) if info else 0o600
    fd, name = tempfile.mkstemp(prefix=".o-ui-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as stream:
            os.fchmod(stream.fileno(), mode)
            if info:
                os.fchown(stream.fileno(), info.st_uid, info.st_gid)
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(name):
            os.unlink(name)

def allows(rule):
    matched = False
    for expr in rule.get("expr", []):
        if "match" in expr:
            m = expr["match"]
            if m.get("op") != "==" or m.get("left") != {"payload": {"protocol": proto, "field": "dport"}}:
                return False
            value = m.get("right")
            def contains(value):
                if type(value) is int:
                    return value == port
                if isinstance(value, dict):
                    if "set" in value:
                        return any(contains(v) for v in value["set"])
                    if "range" in value:
                        lo, hi = value["range"]
                        return lo <= port <= hi
                return False
            if matched:
                return False
            matched = contains(value)
        elif not any(key in expr for key in ("counter", "accept")):
            return False
    return matched and any("accept" in expr for expr in rule.get("expr", []))

def main():
    entries = json.loads(nft("-j", "list", "ruleset"))["nftables"]
    chains = [e["chain"] for e in entries if "chain" in e and e["chain"].get("hook") == "input"]
    rules = [e["rule"] for e in entries if "rule" in e]
    target = ("inet", "host_hardening", "input")
    def identity(obj):
        return obj.get("family"), obj.get("table"), obj.get("chain", obj.get("name"))
    # 旧面板可能遗留空链或纯计数链；检查真实目标内容，不凭链名绕过拦截。
    def harmless_jump(source, expr):
        jump = expr.get("jump")
        if not isinstance(jump, dict) or set(expr) != {"jump"} or set(jump) != {"target"}:
            return False
        key = (source.get("family"), source.get("table"), jump["target"])
        destination = next((e["chain"] for e in entries if "chain" in e and identity(e["chain"]) == key), None)
        if destination is None or any(k in destination for k in ("hook", "policy", "type")):
            return False
        return all(isinstance(x, dict) and len(x) == 1 and set(x) <= {"match", "counter", "return"}
                   for rule in rules if identity(rule) == key for x in rule.get("expr", [None]))
    chain = next((c for c in chains if identity(c) == target), None)
    for c in chains:
        managed = (backend == "ufw" and identity(c) in (("ip", "filter", "INPUT"), ("ip6", "filter", "INPUT"))) or (backend == "firewalld" and c.get("table") == "firewalld")
        if identity(c) == target or managed:
            continue
        def blocks(expr):
            if identity(c) in (("ip", "filter", "INPUT"), ("ip6", "filter", "INPUT")) and expr == {"jump": {"target": "o-ui-iplimit"}}:
                return False
            if harmless_jump(c, expr):
                return False
            return any(k in expr for k in ("drop", "reject", "jump", "goto", "vmap", "queue"))
        blocked = c.get("policy") == "drop" or any(
            identity(r) == identity(c) and any(blocks(x) for x in r.get("expr", [])) for r in rules)
        if blocked:
            raise ValueError("检测到尚不支持的nftables INPUT拦截层，请手动放行 / unsupported nftables INPUT filter; allow manually")
    if chain is None:
        return 4
    if chain.get("type") != "filter":
        raise ValueError("不支持的host_hardening链类型 / unsupported host_hardening chain type")
    records.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    if config.is_symlink() or records.is_symlink() or config.parent.is_symlink() or records.parent.is_symlink():
        raise ValueError("防火墙路径不能是符号链接 / firewall paths must not be symlinks")
    lock_fd = os.open(str(records) + ".lock", os.O_CREAT | os.O_APPEND | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
    with os.fdopen(lock_fd, "a") as lock:
        deadline = time.monotonic() + 3
        while True:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                if time.monotonic() >= deadline:
                    raise ValueError("防火墙正在更新，请重试 / firewall update in progress; retry")
                time.sleep(0.05)
        # 获取锁后重新读取运行规则，避免两个保存操作使用同一旧快照。
        current = json.loads(nft("-j", "list", "chain", *target))["nftables"]
        live = [e["rule"] for e in current if "rule" in e]
        invalid = [{"match": {"op": op, "left": {"ct": {"key": "state"}}, "right": "invalid"}} for op in ("==", "in")]
        for rule in live[:-1]:
            expr = rule.get("expr", [])
            verdicts = {k for e in expr for k in e} & {"drop", "reject", "jump", "goto", "vmap", "queue"}
            if verdicts and not (verdicts == {"drop"} and any(i in expr for i in invalid) and all(set(e) <= {"match", "counter", "drop"} for e in expr)):
                raise ValueError("最终规则前存在额外拦截，请手动检查 / extra blocking rule precedes final rule; inspect manually")
        owned = [r for r in live if r.get("comment") == marker]
        expected_match = {"match": {"op": "==", "left": {"payload": {"protocol": proto, "field": "dport"}}, "right": port}}
        if any(not allows(r) or [x for x in r.get("expr", []) if "match" in x] != [expected_match] for r in owned):
            raise ValueError("o-ui规则已被外部修改，请手动检查 / o-ui rule changed externally; inspect manually")
        previous_records = records.read_text() if records.exists() else ""
        recorded = record in previous_records.splitlines(keepends=True)
        if action == "delete" and not recorded:
            print("不是o-ui添加的nft规则，未改动 / nft rule was not added by o-ui; unchanged")
            return 0
        if action == "allow" and not owned and any(allows(r) for r in live):
            print("已有其他nft规则放行，未接管 / already allowed by another nft rule; unchanged")
            return 0
        previous = config.read_text()
        info = config.stat()
        if info.st_uid != os.geteuid() or info.st_mode & 0o022:
            raise ValueError("防火墙文件必须由root管理且不可由其他用户写入 / firewall file must be root-owned and not writable by other users")
        # 仅支持已核实的单表单链布局，不猜测任意nft脚本或include的插入位置。
        tail = "    counter drop\n  }\n}\n"
        if not previous.startswith("table inet host_hardening {\n  chain input {\n") or not previous.endswith(tail) or previous.count("chain ") != 1:
            raise ValueError("不支持的host_hardening持久化布局，请手动放行 / unsupported persistent firewall layout; allow manually")
        candidate = "".join(l for l in previous.splitlines(keepends=True) if l != line)
        candidate_records = "".join(l for l in previous_records.splitlines(keepends=True) if l != record)
        batch = ""
        if action == "allow":
            candidate = candidate[:-len(tail)] + line + tail
            candidate_records += record
            if not owned:
                if not live or not all(any(k in x for k in ("counter", "drop")) for x in live[-1].get("expr", [])) or not any("drop" in x for x in live[-1].get("expr", [])):
                    raise ValueError("未找到最终drop规则，未改动 / final drop rule missing; unchanged")
                handle = int(live[-1]["handle"])
                batch = f'insert rule inet host_hardening input position {handle} {line.strip()}\n'
        else:
            batch = "".join(f'delete rule inet host_hardening input handle {int(r["handle"])}\n' for r in owned)
        # 只校验整份持久化文件，实际应用仅含本端口的事务，不重载/清空现有规则。
        nft("--check", "--file", "-", data=candidate)
        if batch:
            nft("--check", "--file", "-", data=batch)
        if config.read_text() != previous:
            raise ValueError("防火墙文件已变化，请重试 / firewall file changed; retry")
        try:
            write_atomic(config, candidate)
            write_atomic(records, candidate_records)
            if batch:
                nft("--file", "-", data=batch)
        except Exception:
            write_atomic(config, previous)
            write_atomic(records, previous_records)
            raise
        print(f"已{'放行' if action == 'allow' else '撤销'} {port}/{proto}（nft host_hardening，已持久化） / {port}/{proto} nft host_hardening rule persisted")
    return 0

try:
    sys.exit(main())
except ValueError as error:
    print(str(error))
    sys.exit(3)
except (OSError, KeyError, TypeError, subprocess.SubprocessError):
    print("nftables自动放行或撤销失败，请检查host_hardening布局、权限及python3，必要时手动处理 / nftables update failed; check layout, permissions and python3, or manage manually")
    sys.exit(3)
PY
  case $nft_status in 0|4) ;; *) exit "$nft_status" ;; esac
fi
if [ -z "$backend" ]; then
  [ "${nft_status:-4}" != 0 ] || exit 0
  echo "未检测到启用的UFW/firewalld或受支持的nftables拦截层；如有其他防火墙请手动放行 / no supported active firewall detected; allow manually in other firewalls"
  exit 4
fi
exists() {
  if [ "$backend" = ufw ]; then
    ufw status 2>/dev/null | awk -v r="$rule" -v p="$port" '($1 == r || $1 == p) && $2 == "ALLOW" { f = 1 } END { exit !f }'
  else
    firewall-cmd --permanent --query-port="$rule" >/dev/null 2>&1
  fi
}
umask 077
mkdir -p "${RULES%/*}"
touch "$RULES"
exec 9>>"$RULES.lock"
if command -v flock >/dev/null 2>&1; then flock 9; fi
record="$backend $rule"
recorded=''
if grep -qxF "$record" "$RULES"; then recorded=1; fi
if [ "$action" = allow ]; then
  if exists; then
    if [ -n "$recorded" ]; then echo "$rule 已放行（$backend） / $rule already allowed ($backend)"; else echo "$rule 已有其他规则放行，未改动 / $rule is already allowed by another rule; unchanged"; fi
    exit 0
  fi
  if [ "$backend" = ufw ]; then
    ufw allow "$rule" comment 'o-ui inbound' >/dev/null || { echo "放行$rule失败（ufw） / failed to allow $rule (ufw)"; exit 1; }
  else
    { firewall-cmd --permanent --add-port="$rule" >/dev/null && firewall-cmd --reload >/dev/null; } || { echo "放行$rule失败（firewalld） / failed to allow $rule (firewalld)"; exit 1; }
  fi
  [ -n "$recorded" ] || printf '%s\n' "$record" >>"$RULES"
  echo "已放行 $rule（$backend） / allowed $rule ($backend)"
  exit 0
fi
if [ -z "$recorded" ]; then
  echo "$rule 不是o-ui添加的规则，未改动 / $rule was not added by o-ui; unchanged"
  exit 0
fi
if exists; then
  if [ "$backend" = ufw ]; then
    ufw delete allow "$rule" >/dev/null || { echo "撤销$rule失败（ufw） / failed to remove $rule (ufw)"; exit 1; }
  else
    { firewall-cmd --permanent --remove-port="$rule" >/dev/null && firewall-cmd --reload >/dev/null; } || { echo "撤销$rule失败（firewalld） / failed to remove $rule (firewalld)"; exit 1; }
  fi
fi
grep -vxF "$record" "$RULES" >"$RULES.tmp" || true
mv "$RULES.tmp" "$RULES"
echo "已撤销 $rule（$backend） / removed $rule ($backend)"
EOF
  chown root:root "$tmp"
  chmod 0755 "$tmp"
  mv "$tmp" "$F_HELPER"
}

# sudoers只允许o-ui以root运行助手；先用visudo校验临时文件（含点的文件名不被sudo读取）
deploy_firewall_access() {
  local tmp
  write_firewall_helper
  write_update_helper
  if ! command -v sudo >/dev/null 2>&1 || ! command -v visudo >/dev/null 2>&1; then
    if [ "${1-}" != ask ] || ! ask_yes "未找到sudo；面板创建入站时要用它放行防火墙端口。是否用系统包管理器安装sudo？" "sudo was not found; the panel uses it to open firewall ports for new inbounds. Install sudo with the system package manager?" y || ! pkg_install sudo || ! command -v visudo >/dev/null 2>&1; then
      warn "未安装sudo：面板创建或删除入站时不能自动放行或撤销防火墙端口，请手动处理" "sudo is not installed: the panel cannot open or close firewall ports for inbounds; manage them manually"
      return 0
    fi
  fi
  mkdir -p "$(dirname "$F_SUDOERS")"
  tmp=$(mktemp "$(dirname "$F_SUDOERS")/.o-ui.XXXXXX")
  printf '%s\n' '# o-ui: allow the panel to run its firewall helper (validates every argument) and its update helper (no arguments) as root' "$SERVICE_USER ALL=(root) NOPASSWD: $P_HELPER, $P_UPDATE \"\"" >"$tmp"
  chown root:root "$tmp"
  chmod 0440 "$tmp"
  if visudo -cf "$tmp" >/dev/null 2>&1; then
    mv "$tmp" "$F_SUDOERS"
  else
    rm -f "$tmp"
    warn "sudoers校验未通过，未安装；面板不能自动放行防火墙端口" "the sudoers entry failed validation and was not installed; the panel cannot open firewall ports"
  fi
}

# 卸载时清空IP上限封禁与端口跳跃转发并删除助手与sudoers；purge时按记录撤销o-ui加过的入站规则
remove_firewall_access() {
  local backend rule
  [ ! -x "$F_HELPER" ] || "$F_HELPER" flush >/dev/null 2>&1 || true
  [ ! -x "$F_HELPER" ] || "$F_HELPER" redirect flush >/dev/null 2>&1 || true
  if [ -n "${1-}" ] && [ -x "$F_HELPER" ] && [ -f "$F_FWRULES" ]; then
    while read -r backend rule; do
      case $rule in */tcp|*/udp) "$F_HELPER" delete "${rule%/*}" "${rule#*/}" || true ;; esac
    done <"$F_FWRULES"
    rm -rf "$(dirname "$F_FWRULES")"
  fi
  rm -f "$F_SUDOERS" "$F_HELPER" "$F_UPDATE"
  rm -rf "$F_UPDATESTATE"
}

# ---------- 命令 ----------

ask_port() {
  local p def
  def=$((RANDOM % 30000 + 20000))
  while :; do
    ask p "面板端口（1024–65535）" "Panel port (1024–65535)" "$def"
    case $p in ''|*[!0-9]*) continue ;; esac
    [ ${#p} -le 5 ] && [ "$p" -ge 1024 ] && [ "$p" -le 65535 ] || continue
    if port_in_use "$p"; then warn "端口$p已被占用" "port $p is in use"; continue; fi
    OUI_PORT=$p
    return 0
  done
}

# 访问路径以/开头和结尾，默认随机18位（参照OUI）；/表示根路径
ask_path() {
  local p
  while :; do
    ask p "访问路径（字母数字_-，/为根路径）" "Access path (letters, digits, _ -; / for root)" "/$(random_alnum 18)/"
    p=/${p#/}
    case $p in */) ;; *) p=$p/ ;; esac
    case $p in /) break ;; //*|*[!A-Za-z0-9_/-]*|/*/*/) continue ;; esac
    [ ${#p} -le 66 ] && break
  done
  OUI_PATH=$p
}

ADMIN_USER= ADMIN_PASS= ADMIN_GENERATED=
ask_admin() {
  local again
  while :; do
    ask ADMIN_USER "管理员用户名（3–64位字母数字._-）" "Administrator username (3–64 of letters, digits, . _ -)" "${1:-$(random_alnum 10)}"
    printf '%s' "$ADMIN_USER" | grep -Eq '^[A-Za-z0-9_.-]{3,64}$' && break
  done
  while :; do
    ask_secret ADMIN_PASS "管理员密码（6–72字节；直接回车随机生成）" "Administrator password (6–72 bytes; press Enter to generate)"
    if [ -z "$ADMIN_PASS" ]; then ADMIN_PASS=$(random_alnum 18); ADMIN_GENERATED=1; break; fi
    [ "$(printf '%s' "$ADMIN_PASS" | wc -c)" -ge 6 ] && [ "$(printf '%s' "$ADMIN_PASS" | wc -c)" -le 72 ] || { warn "长度须为6–72字节" "length must be 6–72 bytes"; continue; }
    ask_secret again "再次输入密码" "Repeat the password"
    [ "$again" = "$ADMIN_PASS" ] && break
    warn "两次输入不一致" "passwords do not match"
  done
}

# 由面板程序下载官方最新正式版并校验SHA256后选中（复用面板的核心管理）；失败只警告，不中断安装
CORE_STATE=
install_core() {
  local out
  if [ -f "$F_DATA/core/selection.json" ]; then
    info "检测到已有Xray核心选择，保持不变。" "An existing Xray core selection was found; left unchanged."
    CORE_STATE=kept
    return 0
  fi
  info "正在下载并校验Xray官方最新正式版（不含预发布）…" "Downloading and verifying the latest official stable Xray (no prereleases)…"
  if out=$(as_service "$F_INSTALL/bin/o-ui" -install-core latest -data "$F_DATA/core" 2>&1); then
    printf '%s\n' "$out"
    CORE_STATE=installed
  else
    printf '%s\n' "$out" >&2
    warn "Xray未能自动安装（可能无法访问GitHub），面板照常启动" "Xray could not be installed automatically (GitHub may be unreachable); the panel starts anyway"
    CORE_STATE=failed
  fi
}

# 安装结束与“查看当前设置”共用；参数为仅显示一次的随机密码
show_info() {
  local json='' user=$ADMIN_USER path url='' public cert_type names expiry xray state enabled
  if json=$(panel_show 2>/dev/null); then
    user=$(json_field "$json" username)
    path=$(json_field "$json" basePath)
    [ -z "$path" ] || OUI_PATH=$path
    url=$(json_field "$json" url)
    public=$(json_field "$json" public)
    [ "$public" != false ] || [ -z "$url" ] || OUI_MODE=local
  fi
  [ -n "$url" ] || url=$(panel_url)
  printf '\n%s\n' "$(t '════════ o-ui 当前信息 ════════' '════════ o-ui details ════════')"
  if [ "$OUI_MODE" = local ]; then
    info "访问地址：$url（仅本机，经 ssh -L $OUI_PORT:127.0.0.1:$OUI_PORT root@服务器 访问）" "URL: $url (local only, reach it via ssh -L $OUI_PORT:127.0.0.1:$OUI_PORT root@server)"
  else
    info "访问地址：$url" "URL: $url"
  fi
  info "端口：$OUI_PORT    访问路径：${OUI_PATH:-/}" "Port: $OUI_PORT    Path: ${OUI_PATH:-/}"
  info "用户名：${user:-$(t 未知 unknown)}" "Username: ${user:-unknown}"
  if [ -n "${1-}" ]; then
    info "密码：$1（随机生成，只显示这一次，不会保存到磁盘）" "Password: $1 (generated, shown only once and never written to disk)"
  else
    info "密码：已加密保存，无法查看；忘记时用菜单“重置用户名和密码”" "Password: stored hashed and cannot be shown; use “Reset username and password” if forgotten"
  fi
  case $OUI_MODE in
    acme) if [ "$OUI_RENEW" = timer ]; then cert_type=$(t "Let's Encrypt（自动续期）" "Let's Encrypt (auto renewal)"); else cert_type=$(t "Let's Encrypt（手动续期）" "Let's Encrypt (manual renewal)"); fi ;;
    file) cert_type=$(t 已有证书 'existing certificate') ;;
    *) cert_type=$(t 未启用HTTPS 'HTTPS not enabled') ;;
  esac
  if [ "$OUI_MODE" != local ] && [ -f "$F_TLS/fullchain.pem" ] && command -v openssl >/dev/null 2>&1; then
    names=$(cert_names "$F_TLS/fullchain.pem")
    expiry=$(cert_end "$F_TLS/fullchain.pem")
    info "证书：$cert_type；域名/IP：${names:-?}；到期：${expiry:-?}" "Certificate: $cert_type; names: ${names:-?}; expires: ${expiry:-?}"
  else
    info "证书：$cert_type" "Certificate: $cert_type"
  fi
  xray=''
  [ ! -f "$F_DATA/core/selection.json" ] || xray=$(sed -n 's/.*"selected": *"\([^"]*\)".*/\1/p' "$F_DATA/core/selection.json" | head -n 1)
  info "Xray：${xray:-$(t 未安装（可在面板中安装） 'not installed (install it in the panel)')}" "Xray: ${xray:-not installed (install it in the panel)}"
  if systemctl is-active --quiet o-ui; then state=$(t 运行中 running); else state=$(t 已停止 stopped); fi
  if systemctl is-enabled --quiet o-ui; then enabled=$(t 已启用 enabled); else enabled=$(t 未启用 disabled); fi
  info "服务：$state；开机自启：$enabled" "Service: $state; start at boot: $enabled"
  info "BBR（队列/拥塞控制）：$(bbr_current)" "BBR (qdisc/congestion): $(bbr_current)"
  info "程序版本：${OUI_VERSION:-unknown}；管理：运行 o-ui 打开菜单，o-ui help 查看子命令" "Version: ${OUI_VERSION:-unknown}; manage: run o-ui for the menu, o-ui help for subcommands"
}

cmd_install() {
  parse_pkg_opts "$@"
  need_root
  detect_arch >/dev/null
  need_systemd
  stdin_check
  select_lang
  if installed; then die "已安装；升级请运行 o-ui update，重新配置证书请运行 o-ui cert" "already installed; run o-ui update to upgrade or o-ui cert to reconfigure HTTPS"; fi
  need_cmd tar awk useradd
  command -v runuser >/dev/null 2>&1 || need_cmd setpriv
  prepare_package
  local reuse='' bbr=''
  ask_port
  ask_path
  if [ -f "$F_DATA/panel/panel.sqlite" ]; then
    reuse=1
    info "检测到已有数据目录 $P_DATA，将沿用其中的管理员和业务数据。" "Existing data found in $P_DATA; its administrator and data are kept."
  else
    ask_admin
  fi
  ask_access
  bbr_offer && bbr=1
  info "开始安装…" "Installing…"
  ensure_user
  mkdir -p "$F_ETC"
  chmod 0755 "$F_ETC"
  install_files
  deploy_firewall_access ask
  OUI_VERSION=$(pkg_version "$F_INSTALL")
  write_unit
  systemctl daemon-reload
  [ -n "$reuse" ] || panel_init_admin "$ADMIN_USER" "$ADMIN_PASS" || die "初始化管理员失败" "failed to initialize the administrator"
  conf_save
  # 证书失败不开放HTTP：可重新选择；放弃时面板只听127.0.0.1，之后在菜单“证书管理”配置
  while ! obtain_cert || ! cert_promote || ! panel_enable_https; do
    [ ! -e "$F_ACME/http-paused" ] || die "网站服务尚未恢复，已停止安装；请立即运行 o-ui cert-http-resume 并检查网站" "website service has not been restored; installation stopped; run o-ui cert-http-resume immediately and inspect the website"
    [ "$OUI_MODE" != acme ] || acme_forget "$OUI_HOST"
    if ask_yes "HTTPS证书未配置成功，是否重新填写域名或验证方式并重试？" "The HTTPS certificate was not set up; enter a domain or validation method and retry?" y; then
      ask_access
      continue
    fi
    warn "面板暂以仅本机（127.0.0.1）启动，不开放公网；之后运行 o-ui 在“证书管理”配置证书" "the panel starts local-only (127.0.0.1) with nothing exposed; run o-ui and set up the certificate under Certificates"
    OUI_MODE=local OUI_CHALLENGE= OUI_HTTP_SERVICE= OUI_RENEW=
    panel_local_only >/dev/null || warn "未能写入仅本机访问设置" "could not write the local-only access settings"
    break
  done
  panel_set_path || warn "访问路径未能写入面板，暂为原路径" "the access path could not be written to the panel; the previous path stays"
  conf_save
  [ -z "$bbr" ] || bbr_enable || true
  [ "$OUI_MODE" = local ] || fw_offer install
  [ "$OUI_RENEW" != timer ] || renew_timer_enable
  conf_save
  install_core
  systemctl enable o-ui >/dev/null
  systemctl start o-ui
  wait_active || die "o-ui启动失败，请查看 journalctl -u o-ui" "o-ui failed to start; see journalctl -u o-ui"
  systemctl is-enabled --quiet o-ui || warn "o-ui未设为开机自启，请运行 systemctl enable o-ui" "o-ui is not enabled at boot; run systemctl enable o-ui"
  printf '\n%s\n' "$(t '==== o-ui 安装完成 ====' '==== o-ui installed ====')"
  local shown=''
  [ -z "$ADMIN_GENERATED" ] || shown=$ADMIN_PASS
  show_info "$shown"
}

cmd_update() {
  parse_pkg_opts "$@"
  require_installed
  prepare_package
  # 新包自带的安装脚本与正在运行的不同：交给新脚本完成整个更新，使防火墙助手、服务单元等与新版本一致（只交接一次）。
  if [ -z "${O_UI_UPDATE_HANDOFF:-}" ] && [ -f "$PKG_DIR/install.sh" ] && ! cmp -s "$PKG_DIR/install.sh" "$0"; then
    local handoff=(update --package "$WORK/o-ui-linux-$(detect_arch).tar.gz")
    [ ! -f "$WORK/SHA256SUMS" ] || handoff+=(--sums "$WORK/SHA256SUMS")
    O_UI_UPDATE_HANDOFF=1 bash "$PKG_DIR/install.sh" "${handoff[@]}"
    exit $?
  fi
  local new was_active=''
  new=$(pkg_version "$PKG_DIR")
  info "当前版本 $OUI_VERSION，将更新为 $new；数据保留。" "Current $OUI_VERSION, updating to $new; data is kept."
  systemctl is-active --quiet o-ui && was_active=1
  systemctl stop o-ui || true
  install_files
  deploy_firewall_access
  # 旧版本未装Xray时补装官方最新正式版（已有选择则保持不变）；失败只警告
  install_core
  write_unit
  systemctl daemon-reload
  if [ -n "$was_active" ]; then
    systemctl start o-ui
    if ! wait_active; then
      warn "新版本启动失败，回退到原版本" "the new version failed to start; rolling back"
      systemctl stop o-ui || true
      rm -rf "$F_INSTALL"
      mv "$F_INSTALL.prev" "$F_INSTALL"
      [ ! -f "$F_INSTALL/install.sh" ] || { cp "$F_INSTALL/install.sh" "$F_CMD.tmp"; chmod 0755 "$F_CMD.tmp"; mv "$F_CMD.tmp" "$F_CMD"; }
      systemctl start o-ui
      die "已回退到 $OUI_VERSION；请查看 journalctl -u o-ui" "rolled back to $OUI_VERSION; see journalctl -u o-ui"
    fi
  fi
  OUI_VERSION=$new
  conf_save
  [ "$OUI_RENEW" != timer ] || renew_timer_enable
  info "已更新到 $new；上一版本保留在 ${F_INSTALL#$R}.prev。" "Updated to $new; the previous version is kept in ${F_INSTALL#$R}.prev."
}

cmd_uninstall() {
  local purge=''
  case ${1-} in --purge) purge=1 ;; '') ;; *) die "未知参数：$1" "unknown option: $1" ;; esac
  require_installed
  stdin_check
  if [ -n "$purge" ]; then
    info "将删除o-ui程序、服务、数据（$P_DATA）、证书与配置（$P_ETC）以及服务用户，无法恢复。" \
      "This deletes the o-ui program, services, data ($P_DATA), certificates and config ($P_ETC) and the service user. It cannot be undone."
    local answer
    ask answer "请输入 DELETE 确认" "Type DELETE to confirm" ""
    [ "$answer" = DELETE ] || die "已取消" "cancelled"
  else
    ask_yes "将停止并移除o-ui程序与服务，保留数据 $P_DATA 和 $P_ETC。继续？" "Stop and remove the o-ui program and services, keeping $P_DATA and $P_ETC. Continue?" n || die "已取消" "cancelled"
  fi
  systemctl disable --now o-ui >/dev/null 2>&1 || true
  renew_timer_disable
  rm -f "$F_UNITS/o-ui.service"
  systemctl daemon-reload
  if [ -n "$OUI_FW" ] && ask_yes "移除安装时放行的面板端口 $OUI_PORT/tcp 规则？" "Remove the panel port $OUI_PORT/tcp rule added during install?" y; then fw_remove; fi
  if [ -f "$F_SYSCTL" ] && ask_yes "回退o-ui开启的BBR设置？" "Revert the BBR setting enabled by o-ui?" n; then bbr_disable; fi
  remove_firewall_access "$purge"
  rm -rf "$F_INSTALL" "$F_INSTALL.prev" "$F_INSTALL.new"
  if [ -n "$purge" ]; then
    rm -rf "$F_DATA" "$F_ETC"
    if id -u "$SERVICE_USER" >/dev/null 2>&1; then userdel "$SERVICE_USER" || warn "删除用户$SERVICE_USER失败" "failed to delete user $SERVICE_USER"; fi
  else
    conf_save
  fi
  rm -f "$F_CMD"
  if [ -n "$purge" ]; then info "o-ui已完全删除。" "o-ui has been removed completely."
  else info "o-ui已卸载；数据保留在 $P_DATA 与 $P_ETC，重新安装会沿用。" "o-ui uninstalled; data kept in $P_DATA and $P_ETC and reused on reinstall."; fi
}

cmd_cert() {
  case ${1-} in
    renew)
      require_installed
      [ "$OUI_MODE" = acme ] || die "当前未使用自动申请的证书" "no ACME certificate is configured"
      shift
      local rc
      set +e
      acme_guarded --renew -d "$OUI_HOST" --ecc "$@"
      rc=$?
      set -e
      [ $rc -ne 2 ] || { info "证书尚未到续期时间；强制续期请加 --force" "The certificate is not due yet; add --force to renew now"; return 0; }
      return $rc
      ;;
    --force) CERT_FORCE=1 ;;
    '') ;;
    *) die "用法：o-ui cert [--force | renew [--force]]" "usage: o-ui cert [--force | renew [--force]]" ;;
  esac
  require_installed
  stdin_check
  if [ -n "$CERT_FORCE" ]; then
    ask_yes "强制重新申请会消耗CA签发配额，频繁执行可能被限流；成功后自动写入面板、启用HTTPS并重启。继续？" "Forced reissue consumes CA issuance quota and may hit rate limits; success automatically installs the certificate, enables HTTPS and restarts the panel. Continue?" n || return 0
  fi
  local old_mode=$OUI_MODE old_host=$OUI_HOST old_challenge=$OUI_CHALLENGE old_renew=$OUI_RENEW old_http_service=$OUI_HTTP_SERVICE
  ask_access
  if [ "$old_mode" = acme ] && [ "$old_host" = "$OUI_HOST" ] && [ -z "$CERT_FORCE" ] &&
     { [ "$old_challenge" != "$OUI_CHALLENGE" ] || [ "$old_http_service" != "$OUI_HTTP_SERVICE" ]; }; then
    ask_yes "验证方式或临停授权已变化，需要强制重新签发才能写入续期配置（消耗CA配额）。继续？" "Validation or stop authorization changed; forced reissue is needed to save renewal settings (consumes CA quota). Continue?" n || return 0
    CERT_FORCE=1
  fi
  obtain_cert || die "证书未更新，面板设置保持不变" "certificate not updated; panel settings unchanged"
  systemctl stop o-ui
  if ! cert_promote || ! panel_enable_https; then
    cert_restore_previous || warn "证书恢复失败，请检查备份" "certificate restore failed; inspect the backup"
    systemctl start o-ui || true
    die "写入面板HTTPS设置失败，已尝试恢复原证书与服务" "writing panel HTTPS settings failed; previous certificate and service restoration attempted"
  fi
  conf_save
  [ "$old_mode" != local ] || fw_offer
  conf_save
  systemctl start o-ui
  if ! wait_active; then
    systemctl stop o-ui || true
    cert_restore_previous || warn "证书恢复失败，请检查备份" "certificate restore failed; inspect the backup"
    OUI_MODE=$old_mode OUI_HOST=$old_host OUI_CHALLENGE=$old_challenge OUI_RENEW=$old_renew OUI_HTTP_SERVICE=$old_http_service
    if [ "$old_mode" = local ]; then panel_local_only >/dev/null || true; else panel_enable_https || true; fi
    conf_save
    systemctl start o-ui || true
    die "新证书启用后服务未就绪，已尝试恢复原设置；请查看 journalctl -u o-ui" "the service was not ready after certificate activation; previous settings restoration attempted; see journalctl -u o-ui"
  fi
  renew_timer_enable || die "HTTPS已启用，但自动续期未正常运行，请检查定时器" "HTTPS is enabled but automatic renewal is not running correctly; inspect the timer"
  if [ "$old_mode" = acme ] && [ "$old_host" != "$OUI_HOST" ]; then acme_forget "$old_host"; fi
  info "证书已写入面板并启用HTTPS，自动续期已启用。" "Certificate installed in the panel, HTTPS enabled, and automatic renewal enabled."
  info "面板地址：$(panel_url)" "Panel URL: $(panel_url)"
}

# 供acme.sh续期后调用：修正权限并重启面板以加载新证书
cmd_cert_reload() {
  need_root
  [ "${O_UI_CERT_SETUP:-}" != 1 ] || return 0
  conf_load
  case ${1-} in
    --pending) cert_promote || die "续期证书校验失败，未重启面板" "renewed certificate validation failed; panel not restarted" ;;
    '') cert_pair_valid "$F_TLS/fullchain.pem" "$F_TLS/key.pem" || die "证书校验失败，未重启面板" "certificate validation failed; panel not restarted" ;;
    *) die "续期参数无效" "invalid renewal argument" ;;
  esac
  cert_fix_perms || die "证书文件缺失或为链接" "certificate files missing or links"
  if [ "${O_UI_CERT_SETUP:-}" != 1 ] && systemctl is-active --quiet o-ui; then systemctl restart o-ui; fi
}

cmd_bbr() {
  require_installed
  case ${1-status} in
    status) info "队列/拥塞控制：$(bbr_current)；o-ui设置：$([ -f "$F_SYSCTL" ] && t 已写入 written || t 无 none)" "qdisc/congestion: $(bbr_current); o-ui setting: $([ -f "$F_SYSCTL" ] && t 已写入 written || t 无 none)" ;;
    on) bbr_enable ;;
    off) bbr_disable ;;
    *) die "用法：o-ui bbr [status|on|off]" "usage: o-ui bbr [status|on|off]" ;;
  esac
}

cmd_status() {
  require_installed
  info "版本 $OUI_VERSION；访问 $(panel_url)；证书 ${OUI_MODE:-?}${OUI_RENEW:+/$OUI_RENEW}；BBR $(bbr_current)" \
    "Version $OUI_VERSION; URL $(panel_url); certificate ${OUI_MODE:-?}${OUI_RENEW:+/$OUI_RENEW}; BBR $(bbr_current)"
  systemctl status o-ui --no-pager || true
}

# 修改端口：面板停止后改写监听与HTTPS来源及服务单元，防火墙规则随之询问
cmd_port() {
  require_installed
  stdin_check
  local old=$OUI_PORT
  if [ -n "${1-}" ]; then
    case $1 in *[!0-9]*) die "端口须为1024–65535的数字" "port must be a number 1024–65535" ;; esac
    [ "$1" -ge 1024 ] && [ "$1" -le 65535 ] || die "端口须为1024–65535的数字" "port must be a number 1024–65535"
    ! port_in_use "$1" || die "端口$1已被占用" "port $1 is in use"
    OUI_PORT=$1
  else
    ask_port
  fi
  [ "$OUI_PORT" != "$old" ] || { info "端口未变化。" "Port unchanged."; return 0; }
  systemctl stop o-ui
  if [ "$OUI_MODE" = local ]; then
    panel_local_only >/dev/null || { OUI_PORT=$old; systemctl start o-ui || true; die "修改端口失败，原设置保持" "changing the port failed; previous settings kept"; }
    panel_set_path >/dev/null || warn "访问路径未能写入，暂为根路径" "the access path could not be written; it is / for now"
  else
    panel_enable_https || { OUI_PORT=$old; systemctl start o-ui || true; die "修改端口失败，原设置保持" "changing the port failed; previous settings kept"; }
  fi
  write_unit
  systemctl daemon-reload
  if [ -n "$OUI_FW" ] && ask_yes "移除旧端口 $old/tcp 的放行规则？" "Remove the rule for the old port $old/tcp?" y; then fw_remove "$old"; fi
  conf_save
  [ "$OUI_MODE" = local ] || fw_offer
  conf_save
  systemctl start o-ui
  wait_active || die "o-ui启动失败，请查看 journalctl -u o-ui" "o-ui failed to start; see journalctl -u o-ui"
  info "端口已改为 $OUI_PORT：$(panel_url)" "Port changed to $OUI_PORT: $(panel_url)"
}

cmd_path() {
  require_installed
  stdin_check
  ask_path
  systemctl stop o-ui
  panel_set_path || { systemctl start o-ui || true; die "修改访问路径失败，原设置保持" "changing the access path failed; previous settings kept"; }
  conf_save
  systemctl start o-ui
  wait_active || die "o-ui启动失败，请查看 journalctl -u o-ui" "o-ui failed to start; see journalctl -u o-ui"
  info "访问路径已改为 $OUI_PATH：$(panel_url)" "Access path changed to $OUI_PATH: $(panel_url)"
}

cmd_reset_admin() {
  require_installed
  stdin_check
  local json current=''
  if json=$(panel_show 2>/dev/null); then current=$(json_field "$json" username); fi
  ask_admin "$current"
  systemctl stop o-ui
  panel_reset_admin "$ADMIN_USER" "$ADMIN_PASS" || { systemctl start o-ui || true; die "重置管理员失败，原账号保持" "resetting the administrator failed; the previous account stays"; }
  systemctl start o-ui
  wait_active || die "o-ui启动失败，请查看 journalctl -u o-ui" "o-ui failed to start; see journalctl -u o-ui"
  info "管理员已重置，用户名：$ADMIN_USER；原有登录需重新登录。" "Administrator reset, username: $ADMIN_USER; existing sessions must sign in again."
  [ -z "$ADMIN_GENERATED" ] || info "密码：$ADMIN_PASS（随机生成，只显示这一次）" "Password: $ADMIN_PASS (generated, shown only once)"
}

cmd_cert_menu() {
  local n
  while :; do
    info "证书管理：" "Certificates:"
    menu_item 1 "申请或重新配置ACME证书" "Request or reconfigure an ACME certificate"
    menu_item 2 "立即续期（ACME）" "Renew now (ACME)"
    menu_item 3 "查看当前证书与自动续期状态" "Show certificate and automatic renewal status"
    menu_item 4 "强制重新申请并启用证书" "Force reissue and enable certificate"
    menu_item 0 "返回上一级" "Back to previous menu"
    choose n 0 4 0
    case $n in
      0) return 0 ;;
      1) (cmd_cert) || true ;;
      2) (cmd_cert renew --force) || true ;;
      4) (cmd_cert --force) || true ;;
      3) if [ -f "$F_TLS/fullchain.pem" ] && command -v openssl >/dev/null 2>&1; then
           info "域名/IP：$(cert_names "$F_TLS/fullchain.pem")；到期：$(cert_end "$F_TLS/fullchain.pem")；文件：$P_TLS/fullchain.pem" "Names: $(cert_names "$F_TLS/fullchain.pem"); expires: $(cert_end "$F_TLS/fullchain.pem"); file: $P_TLS/fullchain.pem"
         else
           info "未配置面板证书。" "No panel certificate is configured."
         fi
         systemctl status o-ui-cert-renew.timer --no-pager || true ;;
    esac
    conf_load
  done
}

cmd_bbr_menu() {
  local n
  while :; do
    info "BBR管理：" "BBR management:"
    menu_item 1 "查看" "Status"
    menu_item 2 "开启" "Enable"
    menu_item 3 "回退" "Revert"
    menu_item 0 "返回上一级" "Back to previous menu"
    choose n 0 3 0
    case $n in 0) return 0 ;; 1) cmd_bbr status ;; 2) cmd_bbr on ;; 3) cmd_bbr off ;; esac
  done
}

menu_item() { printf ' %2d) %s\n' "$1" "$(t "$2" "$3")"; }

# 开机自启开关：只改是否随系统启动，不改变当前运行状态
panel_boot_toggle() {
  if systemctl is-enabled --quiet o-ui; then
    ask_yes "o-ui已设为开机自启，是否关闭？" "o-ui starts at boot; turn this off?" n || return 0
    systemctl disable o-ui >/dev/null || die "关闭开机自启失败" "failed to disable start at boot"
    info "已关闭开机自启；当前运行状态不变。" "Start at boot is off; the current running state is unchanged."
  else
    systemctl enable o-ui >/dev/null || die "设置开机自启失败" "failed to enable start at boot"
    info "已设为开机自启；当前运行状态不变。" "Start at boot is on; the current running state is unchanged."
  fi
}

# 面板管理二级菜单（用户2026-10-24要求）：启停、状态、日志、开机自启、更新、卸载
cmd_panel_menu() {
  local n boot
  while :; do
    if systemctl is-enabled --quiet o-ui; then boot=$(t 已启用 enabled); else boot=$(t 未启用 disabled); fi
    info "面板管理：" "Panel management:"
    menu_item 1 "启动" "Start"
    menu_item 2 "停止" "Stop"
    menu_item 3 "重启" "Restart"
    menu_item 4 "状态" "Status"
    menu_item 5 "日志" "Logs"
    menu_item 6 "开机自启（当前：$boot）" "Start at boot (now: $boot)"
    menu_item 7 "更新" "Update"
    menu_item 8 "卸载" "Uninstall"
    menu_item 0 "返回上一级" "Back to previous menu"
    choose n 0 8 0
    case $n in
      0) return 0 ;;
      1) systemctl start o-ui || true ;;
      2) systemctl stop o-ui || true ;;
      3) systemctl restart o-ui || true ;;
      4) (cmd_status) || true ;;
      5) journalctl -u o-ui --no-pager -n 100 || true ;;
      6) (panel_boot_toggle) || true ;;
      7) (cmd_update) || true ;;
      8) (cmd_uninstall) || true ;;
    esac
    installed || return 0
    conf_load
  done
}

# 已安装时不带参数运行o-ui进入的编号菜单；每项在子shell中执行，出错只结束该项
cmd_menu() {
  require_installed
  stdin_check
  local n
  while :; do
    printf '\n%s\n' "$(t '════════ o-ui 管理菜单 ════════' '════════ o-ui menu ════════')"
    menu_item 1 "查看当前设置" "Show current settings"
    menu_item 2 "修改端口" "Change port"
    menu_item 3 "修改访问路径" "Change access path"
    menu_item 4 "重置用户名和密码" "Reset username and password"
    menu_item 5 "证书管理" "Certificates"
    menu_item 6 "面板管理" "Panel management"
    menu_item 7 "BBR" "BBR"
    menu_item 0 "退出" "Exit"
    choose n 0 7 0
    case $n in
      0) return 0 ;;
      1) (show_info) || true ;;
      2) (cmd_port) || true ;;
      3) (cmd_path) || true ;;
      4) (cmd_reset_admin) || true ;;
      5) (cmd_cert_menu) || true ;;
      6) (cmd_panel_menu) || true ;;
      7) (cmd_bbr_menu) || true ;;
    esac
    installed || return 0
    conf_load
  done
}

usage() {
  info "o-ui 管理命令：
  install [--version 2026-10-21 | --package 文件 [--sums SHA256SUMS]]  安装
  update  [同上]          升级程序（保留数据，失败自动回退）
  uninstall [--purge]     卸载（默认保留数据；--purge删除全部）
  start|stop|restart|status|log
  （不带参数）            已安装时打开编号管理菜单
  info                    查看当前设置
  port [端口]             修改面板端口
  path                    修改访问路径
  reset-admin             重置用户名和密码
  cert                    申请ACME证书，自动写入面板并启用HTTPS
  cert --force            强制重新申请并启用（需确认，注意CA限流）
  cert renew [--force]    手动续期ACME证书
  bbr [status|on|off]     查看/开启/回退BBR
  version" "o-ui management:
  install [--version 2026-10-21 | --package FILE [--sums SHA256SUMS]]  install
  update  [same options]  upgrade the program (data kept, automatic rollback on failure)
  uninstall [--purge]     uninstall (data kept by default; --purge deletes everything)
  start|stop|restart|status|log
  (no arguments)          open the numbered menu when installed
  info                    show current settings
  port [PORT]             change the panel port
  path                    change the access path
  reset-admin             reset username and password
  cert                    request ACME certificate, install and enable HTTPS
  cert --force            force reissue and enable (confirmation required; CA limits)
  cert renew [--force]    renew the ACME certificate manually
  bbr [status|on|off]     show/enable/revert BBR
  version"
}

main() {
  local cmd=${1-}
  [ $# -eq 0 ] || shift
  conf_load
  case $cmd in
    '') if installed; then cmd_menu; else cmd_install; fi ;;
    info) require_installed; show_info ;;
    port) cmd_port "$@" ;;
    path) cmd_path ;;
    reset-admin) cmd_reset_admin ;;
    install) cmd_install "$@" ;;
    update|upgrade) cmd_update "$@" ;;
    uninstall) cmd_uninstall "$@" ;;
    start|stop|restart) require_installed; systemctl "$cmd" o-ui ;;
    status) cmd_status ;;
    log|logs) require_installed; journalctl -u o-ui --no-pager -n "${1:-200}" ;;
    cert) cmd_cert "$@" ;;
    cert-reload) cmd_cert_reload "$@" ;;
    cert-cron) need_root; [ "$OUI_MODE" != acme ] || acme_guarded --cron ;;
    cert-http-stop) cmd_cert_http_stop "$@" ;;
    cert-http-resume) cmd_cert_http_resume ;;
    bbr) cmd_bbr "$@" ;;
    version) conf_load; printf '%s\n' "${OUI_VERSION:-unknown}" ;;
    help|-h|--help) usage ;;
    *) usage; exit 1 ;;
  esac
}

main "$@"
exit $?
