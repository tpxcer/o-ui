#!/usr/bin/env bash
# o-ui 安装与管理：安装、升级、卸载、HTTPS证书（acme.sh/Let's Encrypt）与BBR。
# 仅支持使用systemd的Linux amd64/arm64；首次安装后本脚本即/usr/local/bin/o-ui。
# 不安装Xray核心，不修改其他防火墙规则；所有系统改动均先询问。
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
F_INSTALL=$R$P_INSTALL
F_DATA=$R$P_DATA
F_ETC=$R$P_ETC
F_TLS=$R$P_TLS
F_ACME=$R$P_ACME
F_CMD=$R$P_CMD
F_CONF=$F_ETC/o-ui.conf
F_UNITS=$R/etc/systemd/system
F_SYSCTL=$R/etc/sysctl.d/99-o-ui-bbr.conf
F_MODULES=$R/etc/modules-load.d/o-ui-bbr.conf
F_PROC=$R/proc

CONF_KEYS='OUI_LANG OUI_PORT OUI_MODE OUI_HOST OUI_CHALLENGE OUI_RENEW OUI_VERSION OUI_FW OUI_BBR_PREV_QDISC OUI_BBR_PREV_CC OUI_BBR_MODULE'
OUI_LANG= OUI_PORT= OUI_MODE= OUI_HOST= OUI_CHALLENGE= OUI_RENEW= OUI_VERSION= OUI_FW= OUI_BBR_PREV_QDISC= OUI_BBR_PREV_CC= OUI_BBR_MODULE=
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

# choose 变量 默认序号 选项数；选项文本须先由调用方打印
choose() {
  local __var=$1 __def=$2 __max=$3 __ans
  while :; do
    printf '%s [%s]: ' "$(t '请输入序号' 'Enter a number')" "$__def"
    IFS= read -r __ans || die "输入已结束" "input ended"
    [ -n "$__ans" ] || __ans=$__def
    case $__ans in *[!0-9]*|'') continue ;; esac
    if [ "$__ans" -ge 1 ] && [ "$__ans" -le "$__max" ]; then printf -v "$__var" '%s' "$__ans"; return 0; fi
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
    case $value in *[!A-Za-z0-9._:@-]*) die "配置文件含无效值：$key" "invalid value in config: $key" ;; esac
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
  case $OPT_VERSION in ''|v[0-9]*) ;; *) die "版本应形如v1.2.3" "version must look like v1.2.3" ;; esac
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
  local loc=zh_CN.UTF-8
  [ "$OUI_LANG" = en ] && loc=en_US.UTF-8
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
Environment=LANG=$loc
Environment=O_UI_DEFAULT_LANG=$OUI_LANG
ExecStart=$P_INSTALL/o-ui $P_DATA $OUI_PORT
Restart=on-failure
RestartSec=5s
# 只向面板主进程发SIGTERM，由面板自行停止核心并保留“退出时核心在运行则下次自动恢复”的标记
KillMode=mixed
KillSignal=SIGTERM
TimeoutStopSec=30s
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=full
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
ExecStart=$P_ACME/acme.sh --cron --home $P_ACME --config-home $P_ACME/data --cert-home $P_ACME/certs
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
  systemctl enable --now o-ui-cert-renew.timer
}

renew_timer_disable() {
  if [ -f "$F_UNITS/o-ui-cert-renew.timer" ]; then
    systemctl disable --now o-ui-cert-renew.timer >/dev/null 2>&1 || true
    rm -f "$F_UNITS/o-ui-cert-renew.timer" "$F_UNITS/o-ui-cert-renew.service"
    systemctl daemon-reload
  fi
}

# Type=simple启动即active，再确认端口已监听且进程仍在；无ss时只看服务状态
wait_active() {
  local i=0
  while [ $i -lt 20 ]; do
    if systemctl is-active --quiet o-ui && { port_in_use "$OUI_PORT" || { ! command -v ss >/dev/null 2>&1 && [ $i -ge 3 ]; }; }; then
      sleep 1
      systemctl is-active --quiet o-ui && return 0
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

panel_url() {
  if [ "$OUI_MODE" = local ]; then
    printf 'http://127.0.0.1:%s/' "$OUI_PORT"
  elif is_ipv6 "$OUI_HOST"; then
    printf 'https://[%s]:%s/' "$OUI_HOST" "$OUI_PORT"
  else
    printf 'https://%s:%s/' "$OUI_HOST" "$OUI_PORT"
  fi
}

# ---------- 证书 ----------

acme() {
  "$F_ACME/acme.sh" --home "$F_ACME" --config-home "$F_ACME/data" --cert-home "$F_ACME/certs" "$@"
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

# 交互收集证书方式，结果写入OUI_MODE/OUI_HOST/OUI_CHALLENGE/OUI_RENEW及CERT_*变量
CERT_EMAIL= CERT_CF_TOKEN= CERT_CF_ZONE= CERT_SRC_CERT= CERT_SRC_KEY=
ask_access() {
  local n host
  info "面板访问方式：" "Panel access:"
  info "  1) HTTPS + 自动申请证书（ACME/Let's Encrypt，推荐）" "  1) HTTPS + automatic certificate (ACME/Let's Encrypt, recommended)"
  info "  2) HTTPS + 使用已有证书文件" "  2) HTTPS + existing certificate files"
  info "  3) 仅本机（127.0.0.1，经SSH隧道访问，不开放公网）" "  3) Local only (127.0.0.1 via SSH tunnel, nothing exposed)"
  choose n 1 3
  case $n in
    3) OUI_MODE=local; OUI_HOST=; OUI_CHALLENGE=; OUI_RENEW=; return 0 ;;
    2) OUI_MODE=file ;;
    1) OUI_MODE=acme ;;
  esac
  while :; do
    ask host "面板域名或公网IP（证书将签发给它）" "Panel domain or public IP (the certificate is issued for it)" "$OUI_HOST"
    host=$(printf '%s' "$host" | tr 'A-Z' 'a-z')
    case $host in ''|*[!a-z0-9.:-]*) continue ;; esac
    if is_ip "$host" || printf '%s' "$host" | grep -Eq '^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,63}$'; then break; fi
  done
  OUI_HOST=$host
  if [ "$OUI_MODE" = file ]; then
    while :; do
      ask CERT_SRC_CERT "证书文件路径（含完整链的PEM）" "Certificate file path (PEM with full chain)"
      ask CERT_SRC_KEY "私钥文件路径（PEM）" "Private key file path (PEM)"
      [ -f "$CERT_SRC_CERT" ] && [ -f "$CERT_SRC_KEY" ] && [ "$CERT_SRC_CERT" != "$CERT_SRC_KEY" ] && break
      warn "文件不存在或两者相同" "files missing or identical"
    done
    OUI_CHALLENGE=; OUI_RENEW=
    info "已有证书不会自动续期；更换证书后请再运行 o-ui cert。" "Existing certificates are not renewed automatically; run o-ui cert again after replacing them."
    return 0
  fi
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
  if [ "$OUI_CHALLENGE" = http ] && port_in_use 80; then
    warn "80端口已被占用，HTTP-01无法使用；请改用Cloudflare DNS、已有证书或仅本机" "port 80 is in use, HTTP-01 cannot work; use Cloudflare DNS, existing files or local only"
    ask_access
    return 0
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
  info "续期方式：" "Renewal:"
  info "  1) 自动（systemd定时器每6小时检查，续期后重启面板，代理会短暂中断）" "  1) Automatic (systemd timer checks every 6 hours; panel restarts after renewal, proxies drop briefly)"
  info "  2) 手动（到期前运行 o-ui cert renew）" "  2) Manual (run o-ui cert renew before expiry)"
  choose n 1 2
  OUI_RENEW=timer
  if [ "$n" = 2 ]; then OUI_RENEW=manual; fi
}

# 申请或复制证书到/etc/o-ui/tls；失败返回非零
obtain_cert() {
  if [ "$OUI_MODE" = file ]; then
    mkdir -p "$F_TLS"
    cp "$CERT_SRC_CERT" "$F_TLS/fullchain.pem.tmp"
    cp "$CERT_SRC_KEY" "$F_TLS/key.pem.tmp"
    mv "$F_TLS/fullchain.pem.tmp" "$F_TLS/fullchain.pem"
    mv "$F_TLS/key.pem.tmp" "$F_TLS/key.pem"
    cert_fix_perms
    return
  fi
  if [ "$OUI_CHALLENGE" = http ]; then
    command -v socat >/dev/null 2>&1 || command -v python3 >/dev/null 2>&1 || ensure_tool socat socat || {
      warn "HTTP-01需要socat或python3" "HTTP-01 needs socat or python3"; return 1; }
  fi
  acme_ensure
  local rc args
  args=(--issue --server letsencrypt -d "$OUI_HOST" --keylength ec-256)
  [ -z "$CERT_EMAIL" ] || args+=(--accountemail "$CERT_EMAIL")
  if [ "$OUI_CHALLENGE" = cf ]; then args+=(--dns dns_cf); else args+=(--standalone); fi
  is_ipv6 "$OUI_HOST" && args+=(--listen-v6)
  is_ip "$OUI_HOST" && args+=(--cert-profile shortlived --days 3)
  info "正在申请证书（$OUI_HOST）…" "Requesting certificate ($OUI_HOST)…"
  set +e
  (
    umask 077
    if [ "$OUI_CHALLENGE" = cf ]; then
      export CF_Token="$CERT_CF_TOKEN"
      [ -z "$CERT_CF_ZONE" ] || export CF_Zone_ID="$CERT_CF_ZONE"
    fi
    acme "${args[@]}"
  )
  rc=$?
  set -e
  # acme.sh返回2表示证书仍有效、无需重新签发
  [ $rc -eq 0 ] || [ $rc -eq 2 ] || { warn "证书申请失败（acme.sh退出码$rc）" "certificate request failed (acme.sh exit $rc)"; return 1; }
  mkdir -p "$F_TLS"
  acme --install-cert -d "$OUI_HOST" --ecc --key-file "$F_TLS/key.pem" --fullchain-file "$F_TLS/fullchain.pem" \
    --reloadcmd "$P_CMD cert-reload" || { warn "证书导出失败" "certificate install failed"; return 1; }
  cert_fix_perms
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

# ---------- 防火墙：只处理面板端口 ----------

fw_offer() {
  if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qi '^status: active'; then
    if ask_yes "检测到UFW已启用，是否仅放行面板端口 $OUI_PORT/tcp？" "UFW is active; allow only the panel port $OUI_PORT/tcp?" y; then
      ufw allow "$OUI_PORT/tcp" comment 'o-ui panel' >/dev/null && OUI_FW=ufw
    fi
  elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    if ask_yes "检测到firewalld正在运行，是否仅放行面板端口 $OUI_PORT/tcp？" "firewalld is running; allow only the panel port $OUI_PORT/tcp?" y; then
      firewall-cmd --add-port="$OUI_PORT/tcp" >/dev/null && firewall-cmd --permanent --add-port="$OUI_PORT/tcp" >/dev/null && OUI_FW=firewalld
    fi
  else
    info "未检测到启用的UFW/firewalld；如有云安全组或其他防火墙，请自行放行TCP $OUI_PORT。" "No active UFW/firewalld found; open TCP $OUI_PORT in any cloud security group or other firewall yourself."
  fi
  if [ "$OUI_CHALLENGE" = http ]; then
    info "HTTP-01申请与续期时需公网可访问80/tcp；本脚本不改80端口规则。" "HTTP-01 needs public access to 80/tcp during issue and renewal; this script does not change port 80 rules."
  fi
}

fw_remove() {
  case $OUI_FW in
    ufw) ufw delete allow "$OUI_PORT/tcp" >/dev/null || true ;;
    firewalld) firewall-cmd --remove-port="$OUI_PORT/tcp" >/dev/null 2>&1 || true; firewall-cmd --permanent --remove-port="$OUI_PORT/tcp" >/dev/null || true ;;
  esac
  OUI_FW=
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

ADMIN_USER= ADMIN_PASS= ADMIN_GENERATED=
ask_admin() {
  local again
  while :; do
    ask ADMIN_USER "管理员用户名（3–64位字母数字._-）" "Administrator username (3–64 of letters, digits, . _ -)" "admin-$(random_alnum 6 | tr 'A-Z' 'a-z')"
    printf '%s' "$ADMIN_USER" | grep -Eq '^[A-Za-z0-9_.-]{3,64}$' && break
  done
  while :; do
    ask_secret ADMIN_PASS "管理员密码（12–72字节；直接回车随机生成）" "Administrator password (12–72 bytes; press Enter to generate)"
    if [ -z "$ADMIN_PASS" ]; then ADMIN_PASS=$(random_alnum 24); ADMIN_GENERATED=1; break; fi
    [ "$(printf '%s' "$ADMIN_PASS" | wc -c)" -ge 12 ] && [ "$(printf '%s' "$ADMIN_PASS" | wc -c)" -le 72 ] || { warn "长度须为12–72字节" "length must be 12–72 bytes"; continue; }
    ask_secret again "再次输入密码" "Repeat the password"
    [ "$again" = "$ADMIN_PASS" ] && break
    warn "两次输入不一致" "passwords do not match"
  done
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
  OUI_VERSION=$(pkg_version "$F_INSTALL")
  write_unit
  systemctl daemon-reload
  [ -n "$reuse" ] || panel_init_admin "$ADMIN_USER" "$ADMIN_PASS" || die "初始化管理员失败" "failed to initialize the administrator"
  conf_save
  if [ "$OUI_MODE" != local ]; then
    if ! obtain_cert || ! panel_enable_https; then
      warn "HTTPS未配置成功，面板暂以仅本机模式启动；排除问题后运行 o-ui cert" "HTTPS setup failed; the panel starts local-only for now. Fix the issue and run o-ui cert"
      [ "$OUI_MODE" != acme ] || acme_forget "$OUI_HOST"
      OUI_MODE=local OUI_CHALLENGE= OUI_RENEW=
    fi
  fi
  if [ "$OUI_MODE" = local ] && [ -n "$reuse" ]; then
    panel_local_only >/dev/null || warn "未能把已有访问设置恢复为仅本机" "could not reset the existing access settings to local only"
  fi
  conf_save
  [ -z "$bbr" ] || bbr_enable || true
  [ "$OUI_MODE" = local ] || fw_offer
  [ "$OUI_RENEW" != timer ] || renew_timer_enable
  conf_save
  systemctl enable o-ui >/dev/null
  systemctl start o-ui
  wait_active || die "o-ui启动失败，请查看 journalctl -u o-ui" "o-ui failed to start; see journalctl -u o-ui"
  printf '\n%s\n' "$(t '==== o-ui 安装完成 ====' '==== o-ui installed ====')"
  info "版本：$OUI_VERSION" "Version: $OUI_VERSION"
  if [ "$OUI_MODE" = local ]; then
    info "面板仅本机：先在本地电脑运行 ssh -L $OUI_PORT:127.0.0.1:$OUI_PORT root@服务器，再打开 $(panel_url)" \
      "Panel is local only: run ssh -L $OUI_PORT:127.0.0.1:$OUI_PORT root@server on your computer, then open $(panel_url)"
  else
    info "登录地址（HTTPS）：$(panel_url)" "Sign-in URL (HTTPS): $(panel_url)"
  fi
  if [ -z "$reuse" ]; then
    info "用户名：$ADMIN_USER" "Username: $ADMIN_USER"
    if [ -n "$ADMIN_GENERATED" ]; then
      info "密码：$ADMIN_PASS" "Password: $ADMIN_PASS"
      info "（随机生成，只显示这一次，不会保存到磁盘；请立即妥善保存）" "(generated, shown only once and never written to disk; store it safely now)"
    fi
  fi
  info "Xray核心未自动安装，请登录后在面板中选择版本安装。管理命令：o-ui help" "The Xray core is not installed automatically; choose a version in the panel after signing in. Management: o-ui help"
}

cmd_update() {
  parse_pkg_opts "$@"
  require_installed
  prepare_package
  local new was_active=''
  new=$(pkg_version "$PKG_DIR")
  info "当前版本 $OUI_VERSION，将更新为 $new；数据保留。" "Current $OUI_VERSION, updating to $new; data is kept."
  systemctl is-active --quiet o-ui && was_active=1
  systemctl stop o-ui || true
  install_files
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
      acme --renew -d "$OUI_HOST" --ecc "$@"
      rc=$?
      set -e
      [ $rc -ne 2 ] || { info "证书尚未到续期时间；强制续期请加 --force" "The certificate is not due yet; add --force to renew now"; return 0; }
      return $rc
      ;;
    '') ;;
    *) die "用法：o-ui cert [renew [--force]]" "usage: o-ui cert [renew [--force]]" ;;
  esac
  require_installed
  stdin_check
  local old_mode=$OUI_MODE old_host=$OUI_HOST
  ask_access
  if [ "$OUI_MODE" != local ]; then
    obtain_cert || die "证书未更新，面板设置保持不变" "certificate not updated; panel settings unchanged"
  fi
  if [ "$old_mode" = acme ] && { [ "$OUI_MODE" != acme ] || [ "$old_host" != "$OUI_HOST" ]; }; then acme_forget "$old_host"; fi
  systemctl stop o-ui
  if [ "$OUI_MODE" = local ]; then
    panel_local_only || { systemctl start o-ui || true; die "切换为仅本机失败" "switching to local only failed"; }
  else
    panel_enable_https || { systemctl start o-ui || true; die "写入面板HTTPS设置失败，原设置保持" "writing panel HTTPS settings failed; previous settings kept"; }
  fi
  if [ "$OUI_RENEW" = timer ]; then renew_timer_enable; else renew_timer_disable; fi
  conf_save
  [ "$OUI_MODE" = local ] || [ "$old_mode" != local ] || fw_offer
  conf_save
  systemctl start o-ui
  wait_active || die "o-ui启动失败，请查看 journalctl -u o-ui" "o-ui failed to start; see journalctl -u o-ui"
  info "面板地址：$(panel_url)" "Panel URL: $(panel_url)"
}

# 供acme.sh续期后调用：修正权限并重启面板以加载新证书
cmd_cert_reload() {
  need_root
  conf_load
  cert_fix_perms || die "证书文件缺失或为链接" "certificate files missing or links"
  if systemctl is-active --quiet o-ui; then systemctl restart o-ui; fi
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

usage() {
  info "o-ui 管理命令：
  install [--version vX.Y.Z | --package 文件 [--sums SHA256SUMS]]  安装
  update  [同上]          升级程序（保留数据，失败自动回退）
  uninstall [--purge]     卸载（默认保留数据；--purge删除全部）
  start|stop|restart|status|log
  cert                    重新配置HTTPS证书或切换仅本机
  cert renew [--force]    手动续期ACME证书
  bbr [status|on|off]     查看/开启/回退BBR
  version" "o-ui management:
  install [--version vX.Y.Z | --package FILE [--sums SHA256SUMS]]  install
  update  [same options]  upgrade the program (data kept, automatic rollback on failure)
  uninstall [--purge]     uninstall (data kept by default; --purge deletes everything)
  start|stop|restart|status|log
  cert                    reconfigure the HTTPS certificate or switch to local only
  cert renew [--force]    renew the ACME certificate manually
  bbr [status|on|off]     show/enable/revert BBR
  version"
}

main() {
  local cmd=${1-}
  [ $# -eq 0 ] || shift
  conf_load
  case $cmd in
    '') if installed; then usage; else cmd_install; fi ;;
    install) cmd_install "$@" ;;
    update|upgrade) cmd_update "$@" ;;
    uninstall) cmd_uninstall "$@" ;;
    start|stop|restart) require_installed; systemctl "$cmd" o-ui ;;
    status) cmd_status ;;
    log|logs) require_installed; journalctl -u o-ui --no-pager -n "${1:-200}" ;;
    cert) cmd_cert "$@" ;;
    cert-reload) cmd_cert_reload ;;
    bbr) cmd_bbr "$@" ;;
    version) conf_load; printf '%s\n' "${OUI_VERSION:-unknown}" ;;
    help|-h|--help) usage ;;
    *) usage; exit 1 ;;
  esac
}

main "$@"
exit $?
