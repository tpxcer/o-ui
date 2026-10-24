import test from 'node:test';
import assert from 'node:assert/strict';
import { chmod, mkdir, mkdtemp, readdir, readFile, rename, stat, writeFile, rm } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { packBundle } from '../scripts/build-release.mjs';

// install.sh只在临时根目录内运行，systemctl/runuser/acme.sh/sysctl等系统命令全部由桩记录；不触碰本机系统。
// 证书部署校验使用真实openssl生成的临时测试证书。
const installer = fileURLToPath(new URL('../install.sh', import.meta.url));
const startScript = fileURLToPath(new URL('../scripts/start-local.sh', import.meta.url));
const sha = (data: Buffer | string) => createHash('sha256').update(data).digest('hex');

// iptables/ip6tables命令桩：链与规则按“链 参数”记在状态目录（-t nat另记一份），-C按整行匹配；FAKE_NO_NAT6模拟ip6tables没有nat表
const IPTABLES_STUB = `s=$FAKE_STATE; t=$(basename "$0")
echo "$t $*" >>"$s/calls.log"
[ "$1" = -w ] && shift
if [ "$1" = -t ]; then [ "$t" = ip6tables ] && [ -n "$FAKE_NO_NAT6" ] && exit 3; t=$t-$2; shift 2; fi
op=$1; shift
rules="$s/$t-rules"; touch "$rules" "$s/$t-chains"
case $op in
  -N) grep -qx "$1" "$s/$t-chains" && exit 1; echo "$1" >>"$s/$t-chains" ;;
  -F) grep -qx "$1" "$s/$t-chains" || exit 1; grep -v "^$1 " "$rules" >"$rules.tmp" || true; mv "$rules.tmp" "$rules" ;;
  -C) c=$1; shift; grep -qxF "$c $*" "$rules" ;;
  -I) c=$1; shift; [ "$1" = 1 ] && shift; echo "$c $*" >>"$rules" ;;
  -A) c=$1; shift; echo "$c $*" >>"$rules" ;;
  -D) c=$1; shift; grep -qxF "$c $*" "$rules" || exit 1; grep -vxF "$c $*" "$rules" >"$rules.tmp" || true; mv "$rules.tmp" "$rules" ;;
esac`;

const stubs: Record<string, string> = {
  id: `case "$*" in '-u') echo "\${FAKE_UID:-0}" ;; '-u o-ui') [ -f "$FAKE_STATE/user" ] && echo 990 || exit 1 ;; *) exit 1 ;; esac`,
  uname: `case $1 in -s) echo "\${FAKE_OS:-Linux}" ;; -m) echo "\${FAKE_ARCH:-x86_64}" ;; -r) echo "\${FAKE_KERNEL:-6.8.0-45-generic}" ;; esac`,
  useradd: `echo "useradd $*" >>"$FAKE_STATE/calls.log"; : >"$FAKE_STATE/user"`,
  userdel: `echo "userdel $*" >>"$FAKE_STATE/calls.log"; rm -f "$FAKE_STATE/user"`,
  chown: `echo "chown $*" >>"$FAKE_STATE/calls.log"`,
  sleep: `exit 0`,
  socat: `exit 0`,
  flock: `exec python3 - "$@" <<'PY'
import fcntl, sys
try: fcntl.flock(int(sys.argv[-1]), fcntl.LOCK_EX | fcntl.LOCK_NB)
except BlockingIOError: sys.exit(1)
PY`,
  setsid: `exec python3 -c 'import os,sys; os.setsid(); os.execv(sys.argv[1],sys.argv[1:])' "$@"`,
  journalctl: `echo "journalctl $*" >>"$FAKE_STATE/calls.log"`,
  runuser: `echo "runuser LC_ALL=$LC_ALL $*" >>"$FAKE_STATE/calls.log"
[ "$1" = -u ] && [ "$2" = o-ui ] && [ "$3" = -- ] || exit 97
shift 3
exec "$@"`,
  systemctl: `s=$FAKE_STATE
echo "systemctl $*" >>"$s/calls.log"
unit="$O_UI_ROOT_PREFIX/etc/systemd/system/o-ui.service"
case "$1" in
  show) case "$4" in ControlGroup) echo "/system.slice/$2" ;; TriggeredBy) printf '%s' "\${FAKE_WEBSITE_SOCKET:-}" ;; esac; exit 0 ;;
  is-active) for u; do :; done; case $u in nginx.service|apache2.service|httpd.service|caddy.service) [ -f "$s/website-active" ] ;; o-ui-update) [ -f "$s/update-active" ] ;; o-ui-cert-renew.timer) [ -z "$FAKE_TIMER_FAIL" ] && [ -f "$s/timer" ] ;; *) [ -f "$s/active" ] ;; esac; exit ;;
  is-enabled) for u; do :; done; case $u in o-ui-cert-renew.timer) [ -f "$s/timer" ] ;; ufw) [ -f "$s/ufw-enabled" ] ;; *) [ -f "$s/enabled" ] ;; esac; exit ;;
  enable) for u; do :; done; case $u in o-ui) : >"$s/enabled" ;; o-ui-cert-renew.timer) : >"$s/timer" ;; ufw) : >"$s/ufw-enabled" ;; esac; exit 0 ;;
  start|restart)
    case "$2" in nginx.service|apache2.service|httpd.service|caddy.service) [ -z "\${FAKE_WEBSITE_START_FAIL:-}" ] || exit 1; : >"$s/website-active"; exit 0 ;; esac
    [ "$2" = o-ui ] || exit 0
    if [ -n "\${FAKE_FAIL_VERSION:-}" ] && [ "$(cat "$O_UI_ROOT_PREFIX/opt/o-ui/VERSION")" = "$FAKE_FAIL_VERSION" ]; then rm -f "$s/active" "$s/listening"; exit 0; fi
    : >"$s/active"; awk '/^ExecStart=/ { print $NF }' "$unit" >"$s/listening"; exit 0 ;;
  stop) case "$2" in nginx.service|apache2.service|httpd.service|caddy.service) rm -f "$s/website-active"; [ -z "\${FAKE_WEBSITE_STOP_FAIL:-}" ]; exit ;; esac; [ "$2" = o-ui ] && rm -f "$s/active" "$s/listening"; exit 0 ;;
  disable) case " $* " in *" o-ui "*) rm -f "$s/active" "$s/listening" "$s/enabled" ;; esac; exit 0 ;;
  status) echo "o-ui.service fake status"; exit 0 ;;
esac
exit 0`,
  ss: `if [ "$1" = -H ]; then
  [ -z "\${FAKE_SS_FAIL:-}" ] || exit 1
  if [ -f "$FAKE_STATE/website-active" ]; then cat "$FAKE_STATE/website-listeners"; fi
  if grep -qx 80 "$FAKE_STATE/listening-extra" 2>/dev/null; then echo 'LISTEN 0 4096 0.0.0.0:80 0.0.0.0:*'; fi
  exit 0
fi
echo 'State Recv-Q Send-Q Local Address:Port Peer Address:Port'
for p in $(cat "$FAKE_STATE/listening" "$FAKE_STATE/listening-extra" 2>/dev/null); do echo "LISTEN 0 4096 0.0.0.0:$p 0.0.0.0:*"; done`,
  sysctl: `echo "sysctl $*" >>"$FAKE_STATE/calls.log"
[ -z "\${FAKE_SYSCTL_FAIL:-}" ] || exit 1
put() { f="$O_UI_ROOT_PREFIX/proc/sys/$(printf '%s' "\${1%%=*}" | tr . /)"; printf '%s\\n' "\${1#*=}" >"$f"; }
case $1 in
  -p) grep -E '^[a-z]' "$2" | while IFS= read -r kv; do put "$kv"; done ;;
  -w) shift; for kv in "$@"; do put "$kv"; done ;;
esac`,
  modprobe: `echo "modprobe $*" >>"$FAKE_STATE/calls.log"
[ -n "\${FAKE_BBR_MODULE:-}" ] || exit 1
printf 'reno cubic bbr\\n' >"$O_UI_ROOT_PREFIX/proc/sys/net/ipv4/tcp_available_congestion_control"`,
  ufw: `s=$FAKE_STATE
case $1 in
  status) if [ -n "\${FAKE_UFW:-}" ] || [ -f "$s/ufw-active" ]; then echo 'Status: active'; echo; echo 'To Action From'; cat "$s/ufw-rules" 2>/dev/null || true; else echo 'Status: inactive'; fi ;;
  show) [ -z "$FAKE_UFW_SAVED" ] || echo 'ufw allow 443/tcp' ;;
  allow) echo "ufw $*" >>"$s/calls.log"; [ -z "$FAKE_UFW_FAIL" ] || exit 1; echo "$2 ALLOW Anywhere" >>"$s/ufw-rules" ;;
  --force) echo "ufw $*" >>"$s/calls.log"; : >"$s/ufw-active" ;;
  delete) echo "ufw $*" >>"$s/calls.log"; grep -v "^$3 " "$s/ufw-rules" >"$s/ufw-rules.tmp" || true; mv "$s/ufw-rules.tmp" "$s/ufw-rules" ;;
  *) echo "ufw $*" >>"$s/calls.log" ;;
esac`,
  'apt-get': `echo "apt-get $*" >>"$FAKE_STATE/calls.log"; [ -z "$FAKE_PKG_FAIL" ] || exit 1
mkdir -p "$O_UI_ROOT_PREFIX/etc/default"
[ -f "$O_UI_ROOT_PREFIX/etc/default/ufw" ] || printf 'IPV6=yes\\n' >"$O_UI_ROOT_PREFIX/etc/default/ufw"`,
  'iptables-save': `echo "iptables-save" >>"$FAKE_STATE/calls.log"; [ -z "$FAKE_IPTABLES_SAVED" ] || printf '%s\\n' '-A INPUT -j DROP'`,
  'ip6tables-save': `exit 0`,
  'firewall-cmd': `s=$FAKE_STATE
echo "firewall-cmd $*" >>"$s/calls.log"
case "$*" in
  --state) [ -n "\${FAKE_FIREWALLD:-}" ] ;;
  "--permanent --query-port="*) grep -qx "\${2#--query-port=}" "$s/firewalld-ports" 2>/dev/null ;;
  "--permanent --add-port="*) echo "\${2#--add-port=}" >>"$s/firewalld-ports" ;;
  "--permanent --remove-port="*) grep -vx "\${2#--remove-port=}" "$s/firewalld-ports" >"$s/fw.tmp" || true; mv "$s/fw.tmp" "$s/firewalld-ports" ;;
esac`,
  iptables: IPTABLES_STUB,
  ip6tables: IPTABLES_STUB,
  nft: `exec python3 - "$@" <<'PY'
import json, os, pathlib, re, sys
s = pathlib.Path(os.environ['FAKE_STATE'])
args = sys.argv[1:]
with open(s / 'calls.log', 'a') as f: f.write('nft ' + ' '.join(args) + '\\n')
path = s / 'nft.json'
data = json.loads(path.read_text()) if path.exists() else {'nftables': []}
if args == ['list', 'ruleset']:
    if os.environ.get('FAKE_NFT_READ_FAIL'): sys.exit(1)
    if data['nftables']: print('table inet existing { }')
    sys.exit(0)
if args[:2] == ['-j', 'list']:
    result = data
    if args[2] == 'chain':
        family, table, chain = args[3:]
        result = {'nftables': [e for e in data['nftables'] if any(v.get('family') == family and v.get('table') == table and v.get('chain', v.get('name')) == chain for v in e.values())]}
    print(json.dumps(result)); sys.exit(0)
# stdin供命令文档占用，事务内容由shell包装转交临时文件。
text = pathlib.Path(os.environ['NFT_INPUT']).read_text()
if '--check' in args:
    sys.exit(1 if os.environ.get('FAKE_NFT_FAIL') == 'check' else 0)
if os.environ.get('FAKE_NFT_FAIL') == 'apply': sys.exit(1)
for line in text.splitlines():
    m = re.fullmatch(r'insert rule inet host_hardening input position ([0-9]+) (tcp|udp) dport ([0-9]+) counter accept comment "([^"]+)"', line)
    if m:
        handle, proto, port, comment = m.groups()
        new = {'rule': {'family': 'inet', 'table': 'host_hardening', 'chain': 'input', 'handle': max(e.get('rule', {}).get('handle', 0) for e in data['nftables']) + 1, 'comment': comment, 'expr': [{'match': {'op': '==', 'left': {'payload': {'protocol': proto, 'field': 'dport'}}, 'right': int(port)}}, {'counter': {'packets': 0, 'bytes': 0}}, {'accept': None}]}}
        pos = next(i for i,e in enumerate(data['nftables']) if e.get('rule', {}).get('handle') == int(handle))
        data['nftables'].insert(pos, new)
    else:
        m = re.fullmatch(r'delete rule inet host_hardening input handle ([0-9]+)', line)
        if not m: sys.exit(2)
        data['nftables'] = [e for e in data['nftables'] if e.get('rule', {}).get('handle') != int(m.group(1))]
path.write_text(json.dumps(data))
PY`,
  sudo: `echo "sudo $*" >>"$FAKE_STATE/calls.log"`,
  visudo: `echo "visudo $*" >>"$FAKE_STATE/calls.log"
[ -z "\${FAKE_VISUDO_FAIL:-}" ] && grep -qx 'o-ui ALL=(root) NOPASSWD: /usr/local/libexec/o-ui-firewall, /usr/local/libexec/o-ui-update ""' "$2"`,
  'systemd-run': `echo "systemd-run $*" >>"$FAKE_STATE/calls.log"`,
  curl: `out=''; url=''
while [ $# -gt 0 ]; do case $1 in -o) out=$2; shift ;; -*) ;; *) url=$1 ;; esac; shift; done
echo "curl $url" >>"$FAKE_STATE/calls.log"
cp "$FAKE_DOWNLOADS/$(basename "$url")" "$out" 2>/dev/null || exit 22`,
};

// 假o-ui程序：记录参数与标准输入，按约定接口模拟离线CLI的结果；-show-panel输出单行JSON
const fakePanel = `#!/bin/sh
s=$FAKE_STATE
{ printf 'argv'; for a in "$@"; do printf ' [%s]' "$a"; done; printf ' LC_ALL=%s\\n' "$LC_ALL"; } >>"$s/oui.log"
state=''; cert=''; key=''; data=''; user=''; listen=''; origin=''
cmd=$1; value=\${2-}
while [ $# -gt 0 ]; do case $1 in -state) state=$2; shift ;; -data) data=$2; shift ;; -username) user=$2; shift ;; -panel-cert) cert=$2; shift ;; -panel-key) key=$2; shift ;; -panel-listen|-listen) listen=$2; shift ;; -panel-origin) origin=$2; shift ;; esac; shift; done
case $cmd in
  -install-core)
    [ -z "\${FAKE_CORE_RC:-}" ] || { echo 'Official release service returned HTTP 404' >&2; exit "$FAKE_CORE_RC"; }
    mkdir -p "$data"; printf '{\\n  "selected": "26.3.27"\\n}' >"$data/selection.json"
    case $LC_ALL in zh*) echo '已安装并选中Xray 26.3.27（官方包已校验）；未启动代理。' ;; *) echo 'Xray 26.3.27 installed and selected (official package verified); no proxy was started.' ;; esac
    exit 0 ;;
  -init-panel-admin|-reset-panel-admin)
    IFS= read -r pw || exit 9
    printf '%s' "$pw" >"$s/admin-password"; printf '%s' "$user" >"$s/admin-user"
    mkdir -p "$state"; : >"$state/panel.sqlite"
    exit "\${FAKE_INIT_RC:-0}" ;;
  -enable-panel-https)
    [ -z "\${FAKE_HTTPS_RC:-}" ] || exit "$FAKE_HTTPS_RC"
    [ -r "$cert" ] && [ -r "$key" ] || exit 8
    printf '%s %s' "$listen" "$origin" >"$s/https-enabled"; exit 0 ;;
  -set-panel-path) printf '%s' "$value" >"$s/panel-path"; exit 0 ;;
  -recover-panel-access) rm -f "$s/https-enabled" "$s/panel-path"; printf '%s' "$listen" >"$s/local-restored"; exit 0 ;;
  -show-panel)
    public=false; origin=''; path=$(cat "$s/panel-path" 2>/dev/null || echo /); url=''
    if [ -f "$s/https-enabled" ]; then public=true; read -r listen origin <"$s/https-enabled"; url=$origin$path
    elif [ -f "$s/local-restored" ]; then listen=$(cat "$s/local-restored"); url=http://$listen$path; fi
    printf '{"public":%s,"listen":"%s","origin":"%s","basePath":"%s","url":"%s","username":"%s"}\\n' "$public" "$listen" "$origin" "$path" "$url" "$(cat "$s/admin-user" 2>/dev/null)"
    exit 0 ;;
esac
exit 0
`;

// 假acme.sh：install-cert时复制FAKE_ACME_CERT/KEY（真实测试证书），否则写占位文本
const fakeAcme = `#!/bin/sh
{ printf 'acme'; for a in "$@"; do printf ' [%s]' "$a"; done; printf ' CF_Token=%s CF_Zone_ID=%s\\n' "\${CF_Token:+set}" "\${CF_Zone_ID:-}"; } >>"$FAKE_STATE/acme.log"
mode=''; key=''; full=''; pre=''; post=''; validation=''
while [ $# -gt 0 ]; do case $1 in --issue) mode=issue ;; --install-cert) mode=install ;; --renew|--cron) mode=renew ;; --remove) mode=remove ;; --standalone) validation=http ;; --dns) validation=cf; shift ;; --key-file) key=$2; shift ;; --fullchain-file) full=$2; shift ;; --pre-hook) pre=$2; shift ;; --post-hook) post=$2; shift ;; esac; shift; done
case $mode in
  issue|renew)
    rc=\${FAKE_ACME_ISSUE_RC:-0}
    if [ "$mode" = renew ]; then
      rc=\${FAKE_ACME_RENEW_RC:-0}
      pre=$(cat "$FAKE_STATE/acme-pre" 2>/dev/null); post=$(cat "$FAKE_STATE/acme-post" 2>/dev/null)
      validation=$(cat "$FAKE_STATE/acme-validation" 2>/dev/null)
    fi
    # 未到期在钩子之前返回，匹配固定ACME源码顺序。
    [ "$rc" != 2 ] || exit 2
    if [ "$mode" = issue ]; then printf '%s' "$pre" >"$FAKE_STATE/acme-pre"; printf '%s' "$post" >"$FAKE_STATE/acme-post"; printf '%s' "$validation" >"$FAKE_STATE/acme-validation"; fi
    if [ -n "$pre" ]; then eval "$pre" || exit 1; fi
    if [ -n "\${FAKE_ACME_HANG:-}" ]; then
      : >"$FAKE_STATE/acme-waiting"
      exec python3 -c 'import time; time.sleep(60)'
    fi
    if [ -f "$FAKE_STATE/website-active" ] && [ "$validation" = http ]; then exit 77; fi
    echo "validation $mode" >>"$FAKE_STATE/calls.log"
    if [ -n "$post" ] && [ -z "\${FAKE_ACME_SKIP_POST:-}" ]; then eval "$post" || exit 1; fi
    exit "$rc" ;;
  install) if [ -n "\${FAKE_ACME_CERT:-}" ]; then cp "$FAKE_ACME_CERT" "$full"; cp "$FAKE_ACME_KEY" "$key"; else printf 'CERT\\n' >"$full"; printf 'KEY\\n' >"$key"; fi ;;
esac
exit 0
`;

// 仅测试用的已过期证书与私钥（2020-01-01至2020-01-02），验证续期部署拒绝过期产物。
const expiredCert = `-----BEGIN CERTIFICATE-----
MIIBsTCCAVegAwIBAgIUUiaHdGpZf6Jj6MFbZOtkRlIl6q0wCgYIKoZIzj0EAwIw
HjEcMBoGA1UEAwwTZXhwaXJlZC5leGFtcGxlLmNvbTAeFw0yMDAxMDEwMDAwMDBa
Fw0yMDAxMDIwMDAwMDBaMB4xHDAaBgNVBAMME2V4cGlyZWQuZXhhbXBsZS5jb20w
WTATBgcqhkjOPQIBBggqhkjOPQMBBwNCAAQASbeDdMAuep//VErxhVBbTMgXRdSs
/bRto5I5CstQWuXPC0GeaG2pkqX43RpxwWpGfPO7K4bfJClSyJ7yw8ILo3MwcTAd
BgNVHQ4EFgQUk/qoQvxUTjR0gHGCPirRydfl5eIwHwYDVR0jBBgwFoAUk/qoQvxU
TjR0gHGCPirRydfl5eIwDwYDVR0TAQH/BAUwAwEB/zAeBgNVHREEFzAVghNleHBp
cmVkLmV4YW1wbGUuY29tMAoGCCqGSM49BAMCA0gAMEUCIBvK0/wRsGQsDOkC+JxE
H1vUCfHn2gfko2WyDnuIyDFPAiEA5G/ObJZPT3oI6yT5EU6JKyqoEyFE87mVKFzI
60F+ff4=
-----END CERTIFICATE-----
`;
const expiredKey = `-----BEGIN PRIVATE KEY-----
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgtxSK0BHEtMc0tMAk
P4F7a75rj0DxqjXHUQv/GDyQaZuhRANCAAQASbeDdMAuep//VErxhVBbTMgXRdSs
/bRto5I5CstQWuXPC0GeaG2pkqX43RpxwWpGfPO7K4bfJClSyJ7yw8IL
-----END PRIVATE KEY-----
`;

// 生成自签测试证书；names为空时不带SAN（面板不接受）
async function makeCert(dir: string, certName: string, keyName: string, names: string[]) {
  await mkdir(dir, { recursive: true });
  const san = names.length ? ['-addext', 'subjectAltName=' + names.map(n => (/^[0-9.]+$/.test(n) ? 'IP:' : 'DNS:') + n).join(',')] : [];
  execFileSync('openssl', ['req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes', '-keyout', join(dir, keyName), '-out', join(dir, certName), '-days', '30', '-subj', `/CN=${names[0] ?? 'nosan.example.com'}`, ...san], { stdio: 'ignore' });
  return { cert: join(dir, certName), key: join(dir, keyName) };
}

type Options = { arch?: string; version?: string; platform?: string; extra?: Record<string, string>; skipInnerSums?: boolean };

async function makePackage(dir: string, options: Options = {}) {
  const arch = options.arch ?? 'amd64', source = join(dir, `src-${arch}-${options.version ?? 'v0.1.0'}`), root = join(source, 'o-ui');
  const files: Record<string, string> = {
    'o-ui': await readFile(startScript, 'utf8'), 'bin/o-ui': fakePanel, 'dist/index.html': '<html>o-ui</html>',
    'install.sh': await readFile(installer, 'utf8'), VERSION: `${options.version ?? 'v0.1.0'}\n`, PLATFORM: `${options.platform ?? `linux-${arch}`}\n`, ...options.extra,
  };
  for (const [name, data] of Object.entries(files)) {
    await mkdir(dirname(join(root, name)), { recursive: true });
    await writeFile(join(root, name), data, { mode: name.endsWith('o-ui') ? 0o700 : 0o600 });
  }
  const sums = Object.entries(files).map(([name, data]) => `${sha(data)}  ${name}`).join('\n') + '\n';
  if (!options.skipInnerSums) await writeFile(join(root, 'SHA256SUMS'), sums);
  const name = `o-ui-linux-${arch}.tar.gz`, out = join(dir, name);
  packBundle(source, out);
  await writeFile(join(dir, 'SHA256SUMS'), `${sha(await readFile(out))}  ${name}\n`);
  return out;
}

async function sandbox(options: { acme?: boolean } = {}) {
  const base = await mkdtemp(join(tmpdir(), 'o-ui-install-')), root = join(base, 'root'), bin = join(base, 'stub-bin'), state = join(base, 'state'), downloads = join(base, 'downloads');
  for (const dir of [bin, state, downloads, join(root, 'run/systemd/system'), join(root, 'proc/sys/net/core'), join(root, 'proc/sys/net/ipv4'), join(root, 'etc/sysctl.d')]) await mkdir(dir, { recursive: true });
  for (const [name, body] of Object.entries(stubs)) await writeFile(join(bin, name), `#!/bin/sh\n${body}\n`, { mode: 0o755 });
  // nft同时需要读取stdin事务和内嵌Python脚本，先转存事务到独立临时文件。
  await writeFile(join(bin, 'nft'), `#!/bin/sh\nNFT_INPUT=$(mktemp "$FAKE_STATE/nft-input.XXXXXX")\nexport NFT_INPUT\ncase "$*" in *'--file -'*) cat >"$NFT_INPUT" ;; esac\n${stubs.nft}\n`, { mode: 0o755 });
  await writeFile(join(root, 'proc/sys/net/core/default_qdisc'), 'pfifo_fast\n');
  await writeFile(join(root, 'proc/sys/net/ipv4/tcp_congestion_control'), 'cubic\n');
  await writeFile(join(root, 'proc/sys/net/ipv4/tcp_available_congestion_control'), 'reno cubic bbr\n');
  if (options.acme !== false) {
    await mkdir(join(root, 'etc/o-ui/acme'), { recursive: true });
    await writeFile(join(root, 'etc/o-ui/acme/acme.sh'), fakeAcme, { mode: 0o755 });
  }
  const issued = await makeCert(join(base, 'issued'), 'fullchain.pem', 'key.pem', ['example.com']);
  const pkg = await makePackage(downloads);
  const environment = { PATH: `${bin}:/usr/bin:/bin`, HOME: base, LANG: 'C', O_UI_ROOT_PREFIX: root, O_UI_STDIN_ANSWERS: '1', FAKE_STATE: state, FAKE_DOWNLOADS: downloads, FAKE_ACME_CERT: issued.cert, FAKE_ACME_KEY: issued.key };
  const run = (args: string[], answers: string[] = [], env: Record<string, string> = {}) => {
    const script = existsSync(join(root, 'usr/local/bin/o-ui')) && !env.USE_SOURCE ? join(root, 'usr/local/bin/o-ui') : installer;
    const child = spawnSync('bash', [script, ...args], {
      // 旧有场景默认拒绝新增的防火墙安装询问；专项场景在answers里明确同意。
      input: [...answers, 'n'].map(line => line + '\n').join(''), encoding: 'utf8',
      env: { ...environment, ...env },
    });
    return { status: child.status, out: child.stdout + child.stderr };
  };
  const read = (path: string) => readFile(join(root, path), 'utf8');
  const log = async (name: string) => existsSync(join(state, name)) ? readFile(join(state, name), 'utf8') : '';
  // 直接以当前用户执行已安装的防火墙助手（正式环境由o-ui经sudo调用）
  const helper = (args: string[], env: Record<string, string> = {}) => {
    const child = spawnSync(join(root, 'usr/local/libexec/o-ui-firewall'), args, { encoding: 'utf8', env: { PATH: `${bin}:/usr/bin:/bin`, FAKE_STATE: state, ...env } });
    return { status: child.status, out: child.stdout + child.stderr };
  };
  return { base, root, bin, state, downloads, pkg, run, read, log, helper, environment };
}

const conf = async (box: Awaited<ReturnType<typeof sandbox>>) => Object.fromEntries((await box.read('etc/o-ui/o-ui.conf')).trim().split('\n').map(line => line.split('=', 2)));
// 常用问答：语言、端口、访问路径、用户名、密码（回车随机）
const start = (port = '23456', path = '/panelqa/') => ['1', port, path, 'admin-test', ''];
// ACME：域名、HTTP-01、邮箱留空，自动续期
const acme = (host = 'example.com') => [host, '1', ''];

async function website(box: Awaited<ReturnType<typeof sandbox>>, unit = 'nginx.service') {
  await mkdir(join(box.root, 'proc/4242'), { recursive: true });
  await writeFile(join(box.root, 'proc/4242/cgroup'), `0::/system.slice/${unit}\n`);
  await writeFile(join(box.state, 'website-listeners'), 'LISTEN 0 4096 0.0.0.0:80 0.0.0.0:* users:(("website",pid=4242,fd=6))\nLISTEN 0 4096 [::]:80 [::]:* users:(("website",pid=4242,fd=7))\n');
  await writeFile(join(box.state, 'website-active'), '');
}

test('脚本可被bash解析，以main调用收尾避免下载不完整时执行半截脚本，且不再提供仅本机选项', async () => {
  execFileSync('bash', ['-n', installer]);
  const text = await readFile(installer, 'utf8');
  assert.match(text.trimEnd(), /\nmain "\$@"\nexit \$\?$/);
  assert.match(text, /^set -euo pipefail$/m);
  assert.doesNotMatch(text, /curl[^\n]*\|\s*(ba)?sh/, '不得管道执行远程脚本');
  assert.doesNotMatch(text, /3\) 仅本机|3\) Local only/);
  const shellcheck = spawnSync('shellcheck', ['--version']);
  if (shellcheck.status === 0) execFileSync('shellcheck', ['-s', 'bash', installer]);
});

test('全新安装：中文、随机访问路径、HTTP-01域名证书自动续期、BBR与UFW，结束显示完整信息', async () => {
  const box = await sandbox();
  const answers = ['1', '23456', '', 'admin-test', '', 'Example.COM', '1', '', 'y', 'y'];
  const { status, out } = box.run(['install', '--package', box.pkg], answers, { FAKE_UFW: '1' });
  assert.equal(status, 0, out);
  assert.equal(await box.read('usr/local/bin/o-ui'), await readFile(installer, 'utf8'));
  assert.equal((await stat(join(box.root, 'opt/o-ui/bin/o-ui'))).mode & 0o777, 0o755);
  assert.equal((await stat(join(box.root, 'var/lib/o-ui'))).mode & 0o777, 0o700);
  const unit = await box.read('etc/systemd/system/o-ui.service');
  for (const line of ['User=o-ui', 'Group=o-ui', 'ExecStart=/opt/o-ui/o-ui /var/lib/o-ui 23456', 'Restart=on-failure', 'KillMode=mixed', 'KillSignal=SIGTERM', 'Environment=LANG=zh_CN.UTF-8', 'Environment=O_UI_DEFAULT_LANG=zh', 'ReadWritePaths=-/etc/ufw', 'ProtectSystem=full']) assert.ok(unit.includes(line + '\n'), line);
  assert.doesNotMatch(unit, /^NoNewPrivileges=/m, '面板须能经sudo调用防火墙助手');
  const helperFile = join(box.root, 'usr/local/libexec/o-ui-firewall');
  assert.equal((await stat(helperFile)).mode & 0o777, 0o755);
  assert.match(await readFile(helperFile, 'utf8'), new RegExp(`^RULES='${box.root}/var/lib/o-ui-firewall/rules'$`, 'm'));
  assert.equal(await box.read('etc/sudoers.d/o-ui'), '# o-ui: allow the panel to run its firewall helper (validates every argument) and its update helper (no arguments) as root\no-ui ALL=(root) NOPASSWD: /usr/local/libexec/o-ui-firewall, /usr/local/libexec/o-ui-update ""\n');
  assert.equal((await stat(join(box.root, 'etc/sudoers.d/o-ui'))).mode & 0o777, 0o440);
  assert.match(await box.read('etc/systemd/system/o-ui-cert-renew.timer'), /OnCalendar=\*-\*-\* 00\/6:17:00/);
  // 管理员与密码：服务用户离线写入、密码只经标准输入
  const calls = await box.log('calls.log'), panel = await box.log('oui.log'), password = await box.log('admin-password');
  assert.match(password, /^[A-Za-z0-9]{18}$/);
  assert.ok(!panel.includes(password) && !calls.includes(password), '密码不得出现在命令行参数中');
  assert.ok(panel.includes(`argv [-init-panel-admin] [-state] [${box.root}/var/lib/o-ui/panel] [-username] [admin-test] LC_ALL=zh_CN.UTF-8`), panel);
  assert.ok(panel.includes(`argv [-enable-panel-https] [-state] [${box.root}/var/lib/o-ui/panel] [-panel-cert] [${box.root}/etc/o-ui/tls/fullchain.pem] [-panel-key] [${box.root}/etc/o-ui/tls/key.pem] [-panel-listen] [0.0.0.0:23456] [-panel-origin] [https://example.com:23456] [-confirm-https] [确认启用HTTPS非本机访问]`), panel);
  // 默认访问路径：/开头和结尾的18位随机串，写入面板
  const path = await box.log('panel-path');
  assert.match(path, /^\/[A-Za-z0-9]{18}\/$/);
  assert.ok(panel.includes(`argv [-set-panel-path] [${path}] [-state] [${box.root}/var/lib/o-ui/panel]`), panel);
  assert.equal((await conf(box)).OUI_PATH, path);
  // 顺序：管理员、HTTPS、路径、Xray都在首次启动前；先设开机自启再启动
  for (const word of ['-init-panel-admin', '-enable-panel-https', '-set-panel-path', '-install-core latest']) assert.ok(calls.indexOf(word) < calls.indexOf('systemctl start o-ui'), word);
  assert.ok(calls.indexOf('systemctl enable o-ui') < calls.indexOf('systemctl start o-ui'));
  const acmeLog = await box.log('acme.log');
  assert.ok(acmeLog.includes('[--issue] [--server] [letsencrypt] [-d] [example.com] [--keylength] [ec-256] [--standalone]'), acmeLog);
  assert.ok(acmeLog.includes('[--reloadcmd] [/usr/local/bin/o-ui cert-reload --pending]'));
  assert.equal((await stat(join(box.root, 'etc/o-ui/tls/key.pem'))).mode & 0o777, 0o640);
  assert.equal(await box.read('etc/sysctl.d/99-o-ui-bbr.conf'), '# o-ui: BBR，删除本文件并运行 o-ui bbr off 可回退\nnet.core.default_qdisc=fq\nnet.ipv4.tcp_congestion_control=bbr\n');
  assert.match(calls, /^ufw allow 23456\/tcp comment o-ui panel$/m);
  assert.equal((calls.match(/^ufw /gm) ?? []).length, 1);
  assert.match(calls, new RegExp(`^visudo -cf ${box.root}/etc/sudoers.d/\\.o-ui\\.`, 'm'));
  assert.match(calls, new RegExp(`^chown root:root ${box.root}/usr/local/libexec/o-ui-firewall\\.tmp$`, 'm'));
  // 结束信息：地址含路径、用户名、仅一次的随机密码、证书名称与到期、Xray、服务与自启、BBR、管理命令
  assert.match(out, new RegExp(`访问地址：https://example\\.com:23456${path}`));
  assert.match(out, new RegExp(`端口：23456 +访问路径：${path}`));
  assert.match(out, /用户名：admin-test/);
  assert.equal(out.split(password).length - 1, 1);
  assert.match(out, /证书：Let's Encrypt（自动续期）；域名\/IP：example\.com；到期：\w{3} +\d+ [\d:]+ \d{4} GMT/);
  assert.match(out, /Xray：26\.3\.27/);
  assert.match(out, /服务：运行中；开机自启：已启用/);
  assert.match(out, /BBR（队列\/拥塞控制）：fq\/bbr/);
  assert.match(out, /运行 o-ui 打开菜单/);
  assert.doesNotMatch(out, /http:\/\//);
});

test('英文安装：自定义访问路径、Cloudflare DNS验证、自动续期，令牌只经环境变量传给acme.sh', async () => {
  const box = await sandbox();
  const answers = ['2', '30001', 'myPanel', 'ops_admin', 'correct-horse-battery', 'correct-horse-battery', 'panel.example.org', '2', 'cf-secret-token-123', 'zone123', 'ops@example.org', 'n'];
  const { status, out } = box.run(['install', '--package', box.pkg], answers);
  assert.equal(status, 0, out);
  const acmeLog = await box.log('acme.log'), unit = await box.read('etc/systemd/system/o-ui.service');
  assert.match(acmeLog, /\[--dns\] \[dns_cf\].* CF_Token=set CF_Zone_ID=zone123/);
  assert.doesNotMatch(acmeLog + (await box.log('calls.log')) + out, /cf-secret-token-123/);
  assert.ok(unit.includes('Environment=LANG=en_US.UTF-8\n') && unit.includes('Environment=O_UI_DEFAULT_LANG=en\n'));
  assert.equal(await box.log('panel-path'), '/myPanel/');
  assert.equal(await box.log('admin-password'), 'correct-horse-battery');
  assert.ok(!out.includes('correct-horse-battery'), '用户输入的密码不回显');
  assert.match(out, /URL: https:\/\/panel\.example\.org:30001\/myPanel\//);
  assert.match(out, /Password: stored hashed and cannot be shown/);
  assert.match(out, /Certificate: Let's Encrypt \(auto renewal\)/);
  assert.doesNotMatch(out.slice(out.indexOf('2) English') + 10), /[一-鿿]/u, '选英文后不再出现中文提示');
});

test('IP证书只用HTTP-01并选择短期证书，按3天续期', async () => {
  const box = await sandbox();
  const { status, out } = box.run(['install', '--package', box.pkg], [...start('24000', '/'), '203.0.113.10', '', 'n']);
  assert.equal(status, 0, out);
  assert.match(await box.log('acme.log'), /\[-d\] \[203\.0\.113\.10\] \[--keylength\] \[ec-256\] \[--standalone\] \[--cert-profile\] \[shortlived\] \[--days\] \[3\]/);
  assert.match(await box.log('oui.log'), /\[-panel-listen\] \[0\.0\.0\.0:24000\] \[-panel-origin\] \[https:\/\/203\.0\.113\.10:24000\]/);
  assert.equal(await box.log('panel-path'), '/');
  assert.match(out, /访问地址：https:\/\/203\.0\.113\.10:24000\/\n/);
});

test('访问路径只接受单段字母数字_-，非法输入会重问', async () => {
  const box = await sandbox();
  const { status } = box.run(['install', '--package', box.pkg], ['1', '23456', '/a/b/', '/bad path/', '../x', 'good_path-1', 'admin-test', '', ...acme(), 'n']);
  assert.equal(status, 0);
  assert.equal(await box.log('panel-path'), '/good_path-1/');
});

test('安装不再扫描导入已有证书；由ACME签发且默认自动续期，外部证书保持原样', async () => {
  const box = await sandbox();
  const external = await makeCert(join(box.root, 'root/cert/files.example.com'), 'fullchain.pem', 'privkey.pem', ['files.example.com', 'www.files.example.com']);
  const original = await readFile(external.cert), originalKey = await readFile(external.key);
  const { status, out } = box.run(['install', '--package', box.pkg], [...start('27000'), ...acme(), 'n']);
  assert.equal(status, 0, out);
  assert.doesNotMatch(out, /使用本机已有证书|找到以下有效证书|files\.example\.com/);
  assert.deepEqual(await readFile(external.cert), original);
  assert.deepEqual(await readFile(external.key), originalKey);
  assert.equal(await box.read('etc/o-ui/tls/fullchain.pem'), await readFile(box.base + '/issued/fullchain.pem', 'utf8'));
  assert.equal((await stat(join(box.root, 'etc/o-ui/tls/key.pem'))).mode & 0o777, 0o640);
  assert.equal((await conf(box)).OUI_MODE, 'acme');
  assert.equal((await conf(box)).OUI_RENEW, 'timer');
  assert.match(await box.log('calls.log'), /systemctl enable --now o-ui-cert-renew.timer/);
  assert.match(out, /自动续期/);
});

test('历史导入证书在普通升级时保留，不擅自申请或替换', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], [...start('27001'), ...acme(), 'n']).status, 0);
  const config = await box.read('etc/o-ui/o-ui.conf');
  await writeFile(join(box.root, 'etc/o-ui/o-ui.conf'), config.replace('OUI_MODE=acme', 'OUI_MODE=file').replace('OUI_RENEW=timer', 'OUI_RENEW='));
  const original = await box.read('etc/o-ui/tls/fullchain.pem'), acmeBefore = await box.log('acme.log');
  const pkg = await makePackage(box.downloads, { version: 'v0.2.0' });
  const { status, out } = box.run(['update', '--package', pkg]);
  assert.equal(status, 0, out);
  assert.equal((await conf(box)).OUI_MODE, 'file');
  assert.equal(await box.read('etc/o-ui/tls/fullchain.pem'), original);
  assert.equal(await box.log('acme.log'), acmeBefore);
});

test('HTTP-01确认临停网站：只在验证窗口停止，随后恢复并保存未来续期授权', async () => {
  for (const unit of ['nginx.service', 'apache2.service', 'httpd.service', 'caddy.service']) {
    const box = await sandbox();
    await website(box, unit);
    const result = box.run(['install', '--package', box.pkg], [...start(), 'example.com', '1', 'y', '', 'n']);
    assert.equal(result.status, 0, result.out);
    assert.match(result.out, /也会中断它承载的HTTPS网站/);
    assert.equal((await conf(box)).OUI_MODE, 'acme', result.out);
    assert.equal((await conf(box)).OUI_HTTP_SERVICE, unit);
    const calls = await box.log('calls.log');
    const stop = calls.indexOf(`systemctl stop ${unit}`), validation = calls.indexOf('validation issue'), restore = calls.indexOf(`systemctl start ${unit}`);
    assert.ok(stop > 0 && stop < validation && validation < restore && restore < calls.indexOf('-enable-panel-https'), calls);
    assert.doesNotMatch(calls, new RegExp(`systemctl (disable|enable|restart) ${unit}`));
    assert.equal(existsSync(join(box.state, 'website-active')), true);
    assert.equal(existsSync(join(box.root, 'etc/o-ui/acme/http-paused')), false);
    const service = await box.read('etc/systemd/system/o-ui-cert-renew.service');
    assert.match(service, /ExecStart=\/usr\/local\/bin\/o-ui cert-cron/);
    assert.match(service, /ExecStopPost=\/usr\/local\/bin\/o-ui cert-http-resume/);
  }
});

test('HTTP-01签发失败、post钩子缺失及stop部分失败都恢复网站', async () => {
  const failures: Record<string, string>[] = [{ FAKE_ACME_ISSUE_RC: '1' }, { FAKE_ACME_ISSUE_RC: '1', FAKE_ACME_SKIP_POST: '1' }, { FAKE_WEBSITE_STOP_FAIL: '1' }];
  for (const env of failures) {
    const box = await sandbox();
    await website(box);
    const result = box.run(['install', '--package', box.pkg], [...start(), 'example.com', '1', 'y', '', 'n', 'n'], env);
    assert.equal(result.status, 0, result.out); // 明确放弃后仅本机启动，不是证书成功。
    assert.equal((await conf(box)).OUI_MODE, 'local');
    assert.equal((await conf(box)).OUI_HTTP_SERVICE, '');
    assert.equal(existsSync(join(box.state, 'website-active')), true);
    assert.match(await box.log('calls.log'), /systemctl start nginx.service/);
    assert.equal(existsSync(join(box.root, 'etc/o-ui/acme/http-paused')), false);
  }
});

test('HTTP-01恢复失败明确报错并保留救援记录，后续救援不重复签发', async () => {
  const fresh = await sandbox();
  await website(fresh);
  const install = fresh.run(['install', '--package', fresh.pkg], [...start(), 'example.com', '1', 'y', '', 'n'], { FAKE_WEBSITE_START_FAIL: '1' });
  assert.notEqual(install.status, 0, install.out);
  assert.match(install.out, /网站服务尚未恢复，已停止安装/);
  assert.doesNotMatch(install.out, /==== o-ui 安装完成 ====/);
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n']).status, 0);
  await website(box);
  const result = box.run(['cert', '--force'], ['y', 'example.com', '1', 'y', ''], { FAKE_WEBSITE_START_FAIL: '1' });
  assert.notEqual(result.status, 0, result.out);
  assert.match(result.out, /恢复失败/);
  assert.doesNotMatch(result.out, /证书已写入面板并启用HTTPS/);
  assert.equal(await box.read('etc/o-ui/acme/http-paused'), 'nginx.service\n');
  const before = await box.log('acme.log');
  assert.equal(box.run(['cert-http-resume']).status, 0);
  assert.equal(await box.log('acme.log'), before);
  assert.equal(existsSync(join(box.state, 'website-active')), true);
});

test('HTTP-01拒绝授权或未知/Docker/混合/socket监听者时不停止网站，可重选DNS', async () => {
  for (const kind of ['decline', 'docker', 'mixed', 'socket', 'ss-fail']) {
    const box = await sandbox();
    await website(box);
    const env: Record<string, string> = {};
    if (kind === 'docker') await writeFile(join(box.root, 'proc/4242/cgroup'), '0::/system.slice/docker-deadbeef.scope\n');
    if (kind === 'mixed') {
      await mkdir(join(box.root, 'proc/4243'), { recursive: true });
      await writeFile(join(box.root, 'proc/4243/cgroup'), '0::/system.slice/caddy.service\n');
      await writeFile(join(box.state, 'website-listeners'), 'LISTEN 0 4096 0.0.0.0:80 *:* users:(("nginx",pid=4242,fd=6),("caddy",pid=4243,fd=7))\n');
    }
    if (kind === 'socket') env.FAKE_WEBSITE_SOCKET = 'caddy.socket';
    if (kind === 'ss-fail') env.FAKE_SS_FAIL = '1';
    const result = box.run(['install', '--package', box.pkg], [...start(), 'example.com', '1', ...(kind === 'decline' ? [''] : []), 'example.com', '2', 'token-x', '', '', 'n'], env);
    assert.equal(result.status, 0, result.out);
    assert.equal((await conf(box)).OUI_CHALLENGE, 'cf');
    assert.equal((await conf(box)).OUI_HTTP_SERVICE, '');
    assert.doesNotMatch(await box.log('calls.log'), /systemctl (stop|start|restart) (nginx|caddy|docker)/);
  }
});

test('HTTP-01续期：未到期不临停，到期/失败均恢复，监听者改变不得自动扩大授权', async () => {
  const box = await sandbox();
  await website(box);
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), 'example.com', '1', 'y', '', 'n']).status, 0);
  for (const cmd of [['cert', 'renew'], ['cert-cron']]) {
    let before = (await box.log('calls.log')).length;
    const skip = box.run(cmd, [], { FAKE_ACME_RENEW_RC: '2' });
    assert.equal(skip.status, cmd[0] === 'cert-cron' ? 2 : 0, skip.out);
    assert.doesNotMatch((await box.log('calls.log')).slice(before), /systemctl (stop|start) nginx/);
    for (const rc of ['0', '1']) {
      before = (await box.log('calls.log')).length;
      const renew = box.run(cmd, [], { FAKE_ACME_RENEW_RC: rc });
      assert.equal(renew.status, Number(rc), renew.out);
      const calls = (await box.log('calls.log')).slice(before);
      assert.match(calls, /systemctl stop nginx.service/);
      assert.match(calls, /validation renew/);
      assert.match(calls, /systemctl start nginx.service/);
      assert.equal(existsSync(join(box.state, 'website-active')), true);
    }
  }
  await website(box, 'caddy.service');
  const before = (await box.log('calls.log')).length;
  const unauthorized = box.run(['cert', 'renew']);
  assert.notEqual(unauthorized.status, 0, unauthorized.out);
  assert.match(unauthorized.out, /未授权/);
  assert.doesNotMatch((await box.log('calls.log')).slice(before), /systemctl (stop|start) (caddy|nginx)/);
});

test('HTTP-01重新配置时更换方式/授权须确认强制签发，改DNS后不再临停网站', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n']).status, 0);
  await website(box);
  const beforeConfig = await box.read('etc/o-ui/o-ui.conf'), beforeAcme = await box.log('acme.log');
  const cancelled = box.run(['cert'], ['example.com', '1', 'y', '', 'n']);
  assert.equal(cancelled.status, 0, cancelled.out);
  assert.equal(await box.read('etc/o-ui/o-ui.conf'), beforeConfig);
  assert.equal(await box.log('acme.log'), beforeAcme);
  const granted = box.run(['cert'], ['example.com', '1', 'y', '', 'y']);
  assert.equal(granted.status, 0, granted.out);
  assert.equal((await conf(box)).OUI_HTTP_SERVICE, 'nginx.service');
  assert.match((await box.log('acme.log')).slice(beforeAcme.length), /\[--force\]/);
  const before = (await box.log('calls.log')).length;
  const failed = box.run(['cert'], ['example.com', '2', 'token-x', '', '', 'y'], { FAKE_HTTPS_RC: '1' });
  assert.notEqual(failed.status, 0);
  assert.equal((await conf(box)).OUI_CHALLENGE, 'http', '启用失败保留旧安装配置');
  assert.equal(box.run(['cert', 'renew']).status, 0, 'ACME已保存DNS钩子，即使面板回退也不能停网站');
  const dns = box.run(['cert'], ['example.com', '2', 'token-x', '', '', 'y']);
  assert.equal(dns.status, 0, dns.out);
  assert.equal((await conf(box)).OUI_HTTP_SERVICE, '');
  assert.equal(box.run(['cert', 'renew']).status, 0);
  assert.doesNotMatch((await box.log('calls.log')).slice(before), /systemctl (stop|start) nginx/);
});

test('HTTP-01中断时终止本次ACME进程并恢复；同时运行的证书操作被锁拒绝', async () => {
  for (const command of [['cert', 'renew'], ['cert', '--force']]) {
    const box = await sandbox();
    await website(box);
    assert.equal(box.run(['install', '--package', box.pkg], [...start(), 'example.com', '1', 'y', '', 'n']).status, 0);
    const child = spawn('bash', [join(box.root, 'usr/local/bin/o-ui'), ...command], { env: { ...box.environment, FAKE_ACME_HANG: '1' }, stdio: ['pipe', 'pipe', 'pipe'] });
    child.stdin.end(['y', 'example.com', '1', 'y', ''].join('\n') + '\n');
    let output = '';
    child.stdout.on('data', chunk => { output += chunk; });
    child.stderr.on('data', chunk => { output += chunk; });
    const done = new Promise<number | null>(resolve => child.on('exit', resolve));
    try {
      for (let i = 0; i < 200 && !existsSync(join(box.state, 'acme-waiting')); i++) await new Promise(resolve => setTimeout(resolve, 25));
      assert.equal(existsSync(join(box.state, 'acme-waiting')), true, output);
      assert.equal(existsSync(join(box.state, 'website-active')), false);
      const overlap = box.run(['cert', 'renew']);
      assert.notEqual(overlap.status, 0, overlap.out);
      assert.match(overlap.out, /另一项证书操作正在运行/);
      assert.equal(existsSync(join(box.state, 'website-active')), false, '并发操作不得提前恢复网站');
      child.kill('SIGTERM');
      assert.equal(await done, 130, output);
      assert.equal(existsSync(join(box.state, 'website-active')), true, output);
      assert.equal(existsSync(join(box.root, 'etc/o-ui/acme/http-paused')), false);
      assert.equal(box.run(['cert', 'renew']).status, 0, '中断后锁应释放');
    } finally {
      child.kill('SIGTERM');
      await done;
    }
  }
});

test('80端口被占用时拒绝HTTP-01，重新选择DNS且不停止已有服务', async () => {
  const box = await sandbox();
  await writeFile(join(box.state, 'listening-extra'), '80\n');
  const { status, out } = box.run(['install', '--package', box.pkg], [...start('25000'), 'example.com', '1', 'example.com', '2', 'token-x', '', '', 'n']);
  assert.equal(status, 0, out);
  assert.match(out, /80端口已被占用，HTTP-01无法使用/);
  assert.doesNotMatch(await box.log('calls.log'), /systemctl (stop|restart) (nginx|apache|docker)/);
  assert.match(await box.log('acme.log'), /\[--dns\] \[dns_cf\]/);
  assert.equal((await conf(box)).OUI_MODE, 'acme');
});

test('证书申请失败可重新选择方式；放弃时只以本机启动，不开放HTTP', async () => {
  const retry = await sandbox();
  const ok = retry.run(['install', '--package', retry.pkg], [...start('26000'), ...acme(), 'n', 'y', 'retry.example.com', '2', 'token-x', '', '', 'n'], { FAKE_ACME_ISSUE_RC: '1' });
  assert.equal(ok.status, 0, ok.out);
  assert.match(ok.out, /HTTPS证书未配置成功，是否重新填写域名或验证方式并重试/);
  assert.match(await retry.log('acme.log'), /\[--remove\] \[-d\] \[example\.com\] \[--ecc\]/);
  assert.match(await retry.log('acme.log'), /\[--dns\] \[dns_cf\]/);
  assert.equal((await conf(retry)).OUI_MODE, 'local');
  const giveUp = await sandbox();
  const local = giveUp.run(['install', '--package', giveUp.pkg], [...start('26001'), ...acme(), 'n', 'n'], { FAKE_ACME_ISSUE_RC: '1' });
  assert.equal(local.status, 0, local.out);
  assert.doesNotMatch(await giveUp.log('oui.log'), /-enable-panel-https/);
  assert.equal((await conf(giveUp)).OUI_MODE, 'local');
  assert.match(local.out, /面板暂以仅本机（127\.0\.0\.1）启动，不开放公网/);
  assert.match(local.out, /访问地址：http:\/\/127\.0\.0\.1:26001\/panelqa\/（仅本机/);
  assert.doesNotMatch(await giveUp.log('calls.log'), /^ufw /m);
});

test('发布包摘要、路径、链接、平台或包内清单不符时拒绝安装且不改系统', async () => {
  const cases: [string, (box: Awaited<ReturnType<typeof sandbox>>) => Promise<string[]>, RegExp][] = [
    ['摘要不符', async box => { await writeFile(join(box.downloads, 'SHA256SUMS'), `${'0'.repeat(64)}  o-ui-linux-amd64.tar.gz\n`); return ['--package', box.pkg]; }, /摘要不符/],
    ['平台不符', async box => ['--package', await makePackage(join(box.base, 'arm'), { arch: 'arm64' })], /发布清单中没有唯一|平台/],
    ['PLATFORM错误', async box => { const dir = join(box.base, 'bad'); await mkdir(dir); return ['--package', await makePackage(dir, { platform: 'linux-arm64' })]; }, /包平台与本机不符/],
    ['包内清单被改', async box => { const dir = join(box.base, 'inner'); await mkdir(dir); const out = await makePackage(dir); const source = join(dir, 'src-amd64-v0.1.0'); await writeFile(join(source, 'o-ui', 'bin/o-ui'), 'tampered-after-sums'); packBundle(source, out); await writeFile(join(dir, 'SHA256SUMS'), `${sha(await readFile(out))}  o-ui-linux-amd64.tar.gz\n`); return ['--package', out]; }, /包内清单校验失败/],
    ['路径穿越', async box => { const dir = join(box.base, 'evil'); await mkdir(join(dir, 'x'), { recursive: true }); await writeFile(join(dir, 'x', 'evil'), 'x'); const out = join(dir, 'o-ui-linux-amd64.tar.gz'); execFileSync('tar', ['-czf', out, '-C', join(dir, 'x'), 'evil'], { env: { ...process.env, COPYFILE_DISABLE: '1' } }); await writeFile(join(dir, 'SHA256SUMS'), `${sha(await readFile(out))}  o-ui-linux-amd64.tar.gz\n`); return ['--package', out]; }, /包内路径无效/],
    ['符号链接', async box => { const dir = join(box.base, 'link'); await mkdir(dir); const out = await makePackage(dir); const source = join(dir, 'src-amd64-v0.1.0'); execFileSync('ln', ['-s', '/etc/passwd', join(source, 'o-ui', 'dist', 'leak')]); packBundle(source, out); await writeFile(join(dir, 'SHA256SUMS'), `${sha(await readFile(out))}  o-ui-linux-amd64.tar.gz\n`); return ['--package', out]; }, /链接或特殊文件/],
  ];
  for (const [name, prepare, message] of cases) {
    const box = await sandbox();
    const args = await prepare(box);
    const { status, out } = box.run(['install', ...args], [...start(), ...acme(), 'n']);
    assert.notEqual(status, 0, name);
    assert.match(out, message, `${name}: ${out}`);
    assert.equal(existsSync(join(box.root, 'opt/o-ui')), false, name);
    assert.equal(existsSync(join(box.root, 'usr/local/bin/o-ui')), false, name);
    assert.equal(await box.log('calls.log'), '', name);
  }
});

test('从GitHub Release下载最新或指定版本，并按发布清单校验', async () => {
  const box = await sandbox();
  const { status, out } = box.run(['install'], [...start(), ...acme(), 'n'], { O_UI_REPO: 'owner/o-ui' });
  assert.equal(status, 0, out);
  const calls = await box.log('calls.log');
  assert.match(calls, /^curl https:\/\/github\.com\/owner\/o-ui\/releases\/latest\/download\/o-ui-linux-amd64\.tar\.gz$/m);
  assert.match(calls, /^curl https:\/\/github\.com\/owner\/o-ui\/releases\/latest\/download\/SHA256SUMS$/m);
  const second = await sandbox();
  const pinned = second.run(['install', '--version', 'v0.1.0'], [...start(), ...acme(), 'n'], { O_UI_REPO: 'owner/o-ui', FAKE_ARCH: 'aarch64' });
  assert.notEqual(pinned.status, 0);
  assert.match(await second.log('calls.log'), /releases\/download\/v0\.1\.0\/o-ui-linux-arm64\.tar\.gz/);
  const dated = await sandbox();
  dated.run(['install', '--version', '2026-10-21-v1'], [...start(), ...acme(), 'n'], { O_UI_REPO: 'owner/o-ui' });
  assert.match(await dated.log('calls.log'), /releases\/download\/2026-10-21-v1\/o-ui-linux-amd64\.tar\.gz/);
  const badVersion = (await sandbox()).run(['install', '--version', 'latest'], ['1'], { O_UI_REPO: 'owner/o-ui' });
  assert.notEqual(badVersion.status, 0); assert.match(badVersion.out, /版本应形如2026-10-21|version must look like 2026-10-21/);
  const official = await sandbox();
  assert.equal(official.run(['install'], [...start(), ...acme(), 'n']).status, 0);
  assert.match(await official.log('calls.log'), /^curl https:\/\/github\.com\/tpxcer\/o-ui\/releases\/latest\/download\/o-ui-linux-amd64\.tar\.gz$/m);
  const invalid = (await sandbox()).run(['install'], ['1'], { O_UI_REPO: 'not a repo' });
  assert.notEqual(invalid.status, 0); assert.match(invalid.out, /发布仓库无效/);
});

test('acme.sh只按固定版本摘要安装到独立目录，摘要不符时拒绝', async () => {
  const box = await sandbox({ acme: false });
  const dir = join(box.base, 'acme-src', 'acme.sh-3.1.6'); await mkdir(dir, { recursive: true }); await writeFile(join(dir, 'acme.sh'), fakeAcme, { mode: 0o755 });
  execFileSync('tar', ['-czf', join(box.downloads, '3.1.6.tar.gz'), '-C', join(box.base, 'acme-src'), 'acme.sh-3.1.6'], { env: { ...process.env, COPYFILE_DISABLE: '1' } });
  const { status, out } = box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n']);
  assert.notEqual(status, 0);
  assert.match(out, /acme\.sh摘要不符/);
  assert.match(await box.log('calls.log'), /^curl https:\/\/github\.com\/acmesh-official\/acme\.sh\/archive\/refs\/tags\/3\.1\.6\.tar\.gz$/m);
  assert.equal(existsSync(join(box.root, 'etc/o-ui/acme/acme.sh')), false);
  assert.equal(existsSync(join(box.state, 'active')), false);
});

test('升级时新包的安装脚本不同则交给新脚本完成（只交接一次），助手与服务单元随新版本部署', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n']).status, 0);
  const dir = join(box.base, 'handoff'); await mkdir(dir);
  const stub = '#!/bin/bash\necho "handoff O_UI_UPDATE_HANDOFF=$O_UI_UPDATE_HANDOFF $*" >>"$FAKE_STATE/calls.log"\n';
  const pkg = await makePackage(dir, { version: 'v0.2.0', extra: { 'install.sh': stub } });
  const result = box.run(['update', '--package', pkg], []);
  assert.equal(result.status, 0, result.out);
  assert.match(await box.log('calls.log'), /^handoff O_UI_UPDATE_HANDOFF=1 update --package \S+\/o-ui-linux-amd64\.tar\.gz --sums \S+\/SHA256SUMS$/m);
  assert.equal(await box.read('opt/o-ui/VERSION'), 'v0.1.0\n', '交接后由新脚本安装，旧脚本不再继续');
});

test('升级保留数据并保留上一版本；新版本启动失败自动回退', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n']).status, 0);
  await writeFile(join(box.root, 'var/lib/o-ui/panel/marker'), 'keep');
  const unitFile = join(box.root, 'etc/systemd/system/o-ui.service');
  await writeFile(unitFile, (await readFile(unitFile, 'utf8')).replace('Environment=O_UI_DEFAULT_LANG=zh\n', 'Environment=O_UI_DEFAULT_LANG=zh\nEnvironment=O_UI_EXTRA=keep-me\n'));
  await rm(join(box.root, 'usr/local/libexec/o-ui-firewall')); await rm(join(box.root, 'etc/sudoers.d/o-ui'));
  const v2 = join(box.base, 'v2'); await mkdir(v2);
  const ok = box.run(['update', '--package', await makePackage(v2, { version: 'v0.2.0' })], [], { O_UI_STDIN_ANSWERS: '' });
  assert.equal(ok.status, 0, ok.out);
  const unit = await readFile(unitFile, 'utf8');
  assert.ok(unit.includes('Environment=O_UI_EXTRA=keep-me\n') && unit.split('Environment=LANG=').length === 2, unit);
  assert.ok(existsSync(join(box.root, 'usr/local/libexec/o-ui-firewall')) && existsSync(join(box.root, 'etc/sudoers.d/o-ui')), '升级部署防火墙助手与sudoers');
  assert.equal(await box.read('opt/o-ui/VERSION'), 'v0.2.0\n');
  assert.equal(await box.read('opt/o-ui.prev/VERSION'), 'v0.1.0\n');
  assert.equal(await box.read('var/lib/o-ui/panel/marker'), 'keep');
  assert.equal((await conf(box)).OUI_VERSION, 'v0.2.0');
  const v3 = join(box.base, 'v3'); await mkdir(v3);
  const bad = box.run(['update', '--package', await makePackage(v3, { version: 'v0.3.0' })], [], { FAKE_FAIL_VERSION: 'v0.3.0' });
  assert.notEqual(bad.status, 0);
  assert.match(bad.out, /已回退到 v0\.2\.0/);
  assert.equal(await box.read('opt/o-ui/VERSION'), 'v0.2.0\n');
  assert.ok(existsSync(join(box.state, 'active')));
});

test('卸载默认保留数据与证书；重新安装沿用数据；--purge须输入DELETE并删除全部', async () => {
  const box = await sandbox();
  const installed = box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'y', 'y'], { FAKE_UFW: '1' });
  assert.equal(installed.status, 0, installed.out);
  assert.equal((await conf(box)).OUI_FW, 'ufw', installed.out);
  await writeFile(join(box.state, 'ufw-rules'), '52890/tcp ALLOW Anywhere\n');
  assert.equal(box.helper(['allow', '50001', 'tcp'], { FAKE_UFW: '1' }).status, 0);
  const kept = box.run(['uninstall'], ['y', 'y', 'n'], { FAKE_UFW: '1' });
  assert.equal(kept.status, 0, kept.out);
  for (const gone of ['usr/local/libexec/o-ui-firewall', 'etc/sudoers.d/o-ui']) assert.equal(existsSync(join(box.root, gone)), false, gone);
  assert.equal(await box.read('var/lib/o-ui-firewall/rules'), 'ufw 50001/tcp\n', '普通卸载保留已放行规则及其记录');
  for (const gone of ['opt/o-ui', 'usr/local/bin/o-ui', 'etc/systemd/system/o-ui.service', 'etc/systemd/system/o-ui-cert-renew.timer']) assert.equal(existsSync(join(box.root, gone)), false, gone);
  for (const keep of ['var/lib/o-ui/panel/panel.sqlite', 'etc/o-ui/tls/key.pem', 'etc/o-ui/o-ui.conf', 'etc/sysctl.d/99-o-ui-bbr.conf']) assert.ok(existsSync(join(box.root, keep)), keep + kept.out);
  assert.match(await box.log('calls.log'), /^ufw delete allow 23456\/tcp$/m);
  // 重新安装：沿用管理员与核心选择；证书仍可从/etc/o-ui/tls扫描选用
  const again = box.run(['install', '--package', box.pkg], ['1', '23456', '/panelqa/', ...acme(), 'n'], { USE_SOURCE: '1' });
  assert.equal(again.status, 0, again.out);
  assert.match(again.out, /检测到已有数据目录/);
  assert.equal(((await box.log('oui.log')).match(/-init-panel-admin/g) ?? []).length, 1);
  assert.equal(((await box.log('oui.log')).match(/-install-core/g) ?? []).length, 1, '重新安装保持已有核心选择');
  assert.match(again.out, /检测到已有Xray核心选择，保持不变/);
  const refused = box.run(['uninstall', '--purge'], ['yes']);
  assert.notEqual(refused.status, 0);
  const purged = box.run(['uninstall', '--purge'], ['DELETE', 'y'], { FAKE_UFW: '1' });
  assert.equal(purged.status, 0, purged.out);
  assert.match(await box.log('calls.log'), /^ufw delete allow 50001\/tcp$/m);
  assert.doesNotMatch(await box.log('calls.log'), /delete allow 52890/);
  assert.equal(await box.log('ufw-rules'), '52890/tcp ALLOW Anywhere\n');
  assert.equal(existsSync(join(box.root, 'var/lib/o-ui-firewall')), false);
  for (const gone of ['var/lib/o-ui', 'etc/o-ui', 'opt/o-ui', 'usr/local/bin/o-ui', 'etc/sysctl.d/99-o-ui-bbr.conf']) assert.equal(existsSync(join(box.root, gone)), false, gone);
  assert.match(await box.log('calls.log'), /userdel o-ui/);
});

test('BBR：不支持的内核跳过，应用失败撤销，off按记录原值回退', async () => {
  const old = await sandbox();
  const skipped = old.run(['install', '--package', old.pkg], [...start(), ...acme()], { FAKE_KERNEL: '4.4.0-210-generic' });
  assert.equal(skipped.status, 0, skipped.out);
  assert.match(skipped.out, /当前内核不支持BBR/);
  const container = await sandbox();
  assert.equal(container.run(['install', '--package', container.pkg], [...start(), ...acme(), 'y'], { FAKE_SYSCTL_FAIL: '1' }).status, 0);
  assert.equal(existsSync(join(container.root, 'etc/sysctl.d/99-o-ui-bbr.conf')), false);
  const box = await sandbox();
  await writeFile(join(box.root, 'proc/sys/net/ipv4/tcp_available_congestion_control'), 'reno cubic\n');
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'y'], { FAKE_BBR_MODULE: '1' }).status, 0);
  assert.equal(await box.read('etc/modules-load.d/o-ui-bbr.conf'), 'tcp_bbr\n');
  assert.match(box.run(['bbr', 'status']).out, /fq\/bbr/);
  const off = box.run(['bbr', 'off']);
  assert.equal(off.status, 0, off.out);
  assert.equal(existsSync(join(box.root, 'etc/sysctl.d/99-o-ui-bbr.conf')), false);
  assert.equal(await box.read('proc/sys/net/ipv4/tcp_congestion_control'), 'cubic\n');
  assert.equal(await box.read('proc/sys/net/core/default_qdisc'), 'pfifo_fast\n');
});

test('续期回调修正证书权限，仅在面板运行时重启；手动续期区分未到期', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n']).status, 0);
  await chmod(join(box.root, 'etc/o-ui/tls/key.pem'), 0o600);
  const before = (await box.log('calls.log')).split('\n').length;
  assert.equal(box.run(['cert-reload']).status, 0);
  assert.equal((await stat(join(box.root, 'etc/o-ui/tls/key.pem'))).mode & 0o777, 0o640);
  assert.match((await box.log('calls.log')).split('\n').slice(before - 1).join('\n'), /systemctl restart o-ui/);
  await rm(join(box.state, 'active'));
  const stopped = (await box.log('calls.log')).split('\n').length;
  box.run(['cert-reload']);
  assert.doesNotMatch((await box.log('calls.log')).split('\n').slice(stopped - 1).join('\n'), /restart/);
  const notDue = box.run(['cert', 'renew'], [], { FAKE_ACME_RENEW_RC: '2' });
  assert.equal(notDue.status, 0); assert.match(notDue.out, /尚未到续期时间/);
});

test('o-ui cert：更换ACME域名，先停面板再离线写入，移除旧条目并保持自动续期', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n']).status, 0);
  const { status, out } = box.run(['cert'], acme('later.example.com'));
  assert.equal(status, 0, out);
  const calls = await box.log('calls.log'), https = calls.lastIndexOf('-enable-panel-https');
  assert.ok(calls.lastIndexOf('systemctl stop o-ui', https) > 0 && calls.indexOf('systemctl start o-ui', https) > https);
  assert.match(await box.log('acme.log'), /\[--remove\] \[-d\] \[example\.com\] \[--ecc\]/);
  assert.equal(existsSync(join(box.root, 'etc/systemd/system/o-ui-cert-renew.timer')), true);
  assert.match(out, /https:\/\/later\.example\.com:23456\/panelqa\//);
  assert.equal((await conf(box)).OUI_MODE, 'acme');
  assert.equal((await conf(box)).OUI_RENEW, 'timer');
});

test('续期产物先验证：过期、不配对和损坏证书不得覆盖在线文件或重启，正确产物才部署', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n']).status, 0);
  const cert = await box.read('etc/o-ui/tls/fullchain.pem'), key = await box.read('etc/o-ui/tls/key.pem');
  const other = await makeCert(join(box.base, 'other'), 'cert.pem', 'key.pem', ['example.com']);
  for (const [c, k] of [[expiredCert, expiredKey], [cert, await readFile(other.key, 'utf8')], ['broken', key]]) {
    await writeFile(join(box.root, 'etc/o-ui/acme/deploy/fullchain.pem'), c);
    await writeFile(join(box.root, 'etc/o-ui/acme/deploy/key.pem'), k);
    const before = await box.log('calls.log');
    const result = box.run(['cert-reload', '--pending']);
    assert.notEqual(result.status, 0, result.out);
    assert.equal(await box.read('etc/o-ui/tls/fullchain.pem'), cert);
    assert.equal(await box.read('etc/o-ui/tls/key.pem'), key);
    assert.doesNotMatch((await box.log('calls.log')).slice(before.length), /systemctl restart/);
  }
  await writeFile(join(box.root, 'etc/o-ui/acme/deploy/fullchain.pem'), await readFile(other.cert));
  await writeFile(join(box.root, 'etc/o-ui/acme/deploy/key.pem'), await readFile(other.key));
  assert.equal(box.run(['cert-reload', '--pending']).status, 0);
  assert.equal(await box.read('etc/o-ui/tls/fullchain.pem'), await readFile(other.cert, 'utf8'));
  assert.equal((await stat(join(box.root, 'etc/o-ui/tls/key.pem'))).mode & 0o777, 0o640);
});

test('自动续期定时器失败不能显示安装成功', async () => {
  const box = await sandbox();
  const result = box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n'], { FAKE_TIMER_FAIL: '1' });
  assert.notEqual(result.status, 0);
  assert.match(result.out, /自动续期定时器未正常启用/);
  assert.doesNotMatch(result.out, /==== o-ui 安装完成 ====/);
});

test('强制重新申请需确认，使用issue --force，成功后写入面板并启用HTTPS、自动续期和重启', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n']).status, 0);
  const oldLog = await box.log('acme.log');
  const cancelled = box.run(['cert', '--force'], ['n']);
  assert.equal(cancelled.status, 0, cancelled.out);
  assert.equal(await box.log('acme.log'), oldLog);
  const before = (await box.log('calls.log')).length;
  const result = box.run(['cert', '--force'], ['y', ...acme()]);
  assert.equal(result.status, 0, result.out);
  assert.match((await box.log('acme.log')).slice(oldLog.length), /\[--issue\].*\[--force\]/);
  const calls = (await box.log('calls.log')).slice(before);
  const enable = calls.indexOf('-enable-panel-https');
  assert.ok(enable >= 0 && calls.indexOf('systemctl stop o-ui') < enable && calls.indexOf('systemctl start o-ui') > enable, calls);
  assert.match(calls, /systemctl enable --now o-ui-cert-renew.timer/);
  assert.match(result.out, /证书已写入面板并启用HTTPS，自动续期已启用/);
  const menu = box.run([], ['5', '0', '0']);
  assert.equal(menu.status, 0, menu.out);
  assert.match(menu.out, /4\) 强制重新申请并启用证书/);
  assert.match(menu.out, /0\) 返回上一级/);
});

test('重新申请写入面板失败时恢复旧证书、保持旧配置并恢复服务，不宣称启用成功', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n']).status, 0);
  const cert = await box.read('etc/o-ui/tls/fullchain.pem'), key = await box.read('etc/o-ui/tls/key.pem'), config = await box.read('etc/o-ui/o-ui.conf');
  const next = await makeCert(join(box.base, 'new-issued'), 'fullchain.pem', 'key.pem', ['later.example.com']);
  const result = box.run(['cert', '--force'], ['y', ...acme('later.example.com')], { FAKE_ACME_CERT: next.cert, FAKE_ACME_KEY: next.key, FAKE_HTTPS_RC: '1' });
  assert.notEqual(result.status, 0);
  assert.match(result.out, /写入面板HTTPS设置失败/);
  assert.doesNotMatch(result.out, /证书已写入面板并启用HTTPS/);
  assert.equal(await box.read('etc/o-ui/tls/fullchain.pem'), cert);
  assert.equal(await box.read('etc/o-ui/tls/key.pem'), key);
  assert.equal(await box.read('etc/o-ui/o-ui.conf'), config);
  assert.equal(existsSync(join(box.state, 'active')), true);
});

test('首次安装整套UFW：先允许当前SSH、面板及HTTP-01，再默认拒绝并启用、自启和部署助手', async () => {
  const box = await sandbox();
  const result = box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n', 'y'], { SSH_CONNECTION: '192.0.2.1 45678 192.0.2.2 5522' });
  assert.equal(result.status, 0, result.out);
  const calls = await box.log('calls.log'), enable = calls.indexOf('ufw --force enable');
  assert.ok(enable > 0, calls);
  for (const rule of ['ufw allow 5522/tcp', 'ufw allow 23456/tcp', 'ufw allow 80/tcp', 'ufw default deny incoming', 'ufw default allow outgoing']) assert.ok(calls.indexOf(rule) >= 0 && calls.indexOf(rule) < enable, rule);
  assert.match(calls, /apt-get install -y ufw sudo nftables python3 iptables/);
  assert.match(calls, /systemctl enable ufw/);
  assert.equal((await conf(box)).OUI_FW, 'ufw');
  assert.match(result.out, /UFW已启用并持久化/);
  const backups = (await readdir(join(box.root, 'etc/o-ui'))).filter(n => n.startsWith('firewall-backup.'));
  assert.equal(backups.length, 1);
  assert.equal((await stat(join(box.root, 'etc/o-ui', backups[0]))).mode & 0o777, 0o700);
});

test('DNS验证无需80端口；缺少SSH环境时必须明确输入SSH端口', async () => {
  const box = await sandbox();
  const result = box.run(['install', '--package', box.pkg], [...start(), 'example.com', '2', 'token-test', '', '', 'n', 'y', '2200']);
  assert.equal(result.status, 0, result.out);
  assert.match(await box.log('calls.log'), /ufw allow 2200\/tcp/);
  assert.doesNotMatch(await box.log('calls.log'), /ufw allow 80\/tcp/);
});

test('拒绝整套防火墙或SSH端口无效时不安装、不启用', async () => {
  for (const port of ['', '0', '65536', '0022', '22;true']) {
    const box = await sandbox();
    const result = box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n', port ? 'y' : 'n', port]);
    assert.equal(result.status, 0, result.out);
    assert.doesNotMatch(await box.log('calls.log'), /apt-get|ufw (allow|default|--force)/);
  }
});

test('已有nft、iptables或未启用UFW保存规则时不安装第二套；读取失败也不启用', async () => {
  for (const type of ['nft', 'iptables', 'saved', 'read-failure']) {
    const box = await sandbox();
    if (type === 'nft') await writeFile(join(box.state, 'nft.json'), JSON.stringify({ nftables: [{ table: { family: 'inet', name: 'existing' } }] }));
    const env = { SSH_CONNECTION: '192.0.2.1 45678 192.0.2.2 5522', ...(type === 'iptables' ? { FAKE_IPTABLES_SAVED: '1' } : {}), ...(type === 'saved' ? { FAKE_UFW_SAVED: '1' } : {}), ...(type === 'read-failure' ? { FAKE_NFT_READ_FAIL: '1' } : {}) };
    const result = box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n', 'y'], env);
    assert.equal(result.status, 0, result.out);
    assert.doesNotMatch(await box.log('calls.log'), /ufw (allow|default|--force)/);
    if (type !== 'read-failure') assert.doesNotMatch(await box.log('calls.log'), /apt-get/);
  }
});

test('依赖安装、规则写入或IPv6检查失败都不能启用UFW', async () => {
  for (const type of ['package', 'allow', 'ipv6']) {
    const box = await sandbox();
    if (type === 'ipv6') { await mkdir(join(box.root, 'etc/default'), { recursive: true }); await writeFile(join(box.root, 'etc/default/ufw'), 'IPV6=no\n'); }
    const result = box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n', 'y'], { SSH_CONNECTION: '192.0.2.1 45678 192.0.2.2 5522', ...(type === 'package' ? { FAKE_PKG_FAIL: '1' } : {}), ...(type === 'allow' ? { FAKE_UFW_FAIL: '1' } : {}) });
    assert.equal(result.status, 0, result.out);
    assert.doesNotMatch(await box.log('calls.log'), /ufw --force enable/);
    assert.doesNotMatch(result.out, /UFW已启用并持久化/);
  }
});

test('已安装时o-ui进入编号菜单：查看信息、改访问路径、重置管理员、改端口后退出', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n', 'y'], { FAKE_UFW: '1' }).status, 0);
  const answers = ['1', '3', 'newPath', '4', 'newadmin', '', '2', '34567', 'y', 'y', '0'];
  const { status, out } = box.run([], answers, { FAKE_UFW: '1' });
  assert.equal(status, 0, out);
  for (const item of ['1) 查看当前设置', '2) 修改端口', '3) 修改访问路径', '4) 重置用户名和密码', '5) 证书管理', '6) 启动', '10) 日志', '11) 更新', '12) 卸载', '13) BBR', '0) 退出']) assert.ok(out.includes(item), item);
  assert.match(out, /访问地址：https:\/\/example\.com:23456\/panelqa\//);
  assert.match(out, /用户名：admin-test/);
  assert.match(out, /密码：已加密保存，无法查看/);
  const panel = await box.log('oui.log'), calls = await box.log('calls.log');
  assert.ok(panel.includes(`argv [-set-panel-path] [/newPath/] [-state] [${box.root}/var/lib/o-ui/panel]`), panel);
  assert.match(out, /访问路径已改为 \/newPath\/：https:\/\/example\.com:23456\/newPath\//);
  assert.ok(panel.includes(`argv [-reset-panel-admin] [-state] [${box.root}/var/lib/o-ui/panel] [-username] [newadmin]`), panel);
  const password = await box.log('admin-password');
  assert.match(password, /^[A-Za-z0-9]{18}$/);
  assert.equal(out.split(password).length - 1, 1);
  assert.match(panel, /\[-panel-listen\] \[0\.0\.0\.0:34567\] \[-panel-origin\] \[https:\/\/example\.com:34567\]/);
  assert.ok((await box.read('etc/systemd/system/o-ui.service')).includes('ExecStart=/opt/o-ui/o-ui /var/lib/o-ui 34567\n'));
  assert.match(calls, /^ufw delete allow 23456\/tcp$/m);
  assert.match(calls, /^ufw allow 34567\/tcp comment o-ui panel$/m);
  assert.match(out, /端口已改为 34567：https:\/\/example\.com:34567\/newPath\//);
  assert.deepEqual([(await conf(box)).OUI_PORT, (await conf(box)).OUI_PATH], ['34567', '/newPath/']);
  assert.ok(existsSync(join(box.state, 'active')));
});

test('o-ui info显示完整信息；菜单回车即退出；未安装时o-ui进入安装', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n']).status, 0);
  const info = box.run(['info']);
  assert.equal(info.status, 0, info.out);
  for (const line of [/访问地址：https:\/\/example\.com:23456\/panelqa\//, /用户名：admin-test/, /证书：Let's Encrypt（自动续期）；域名\/IP：example\.com/, /Xray：26\.3\.27/, /服务：运行中；开机自启：已启用/, /BBR/]) assert.match(info.out, line);
  // 证书文件损坏或Xray未安装时信息页仍完整输出
  await writeFile(join(box.root, 'etc/o-ui/tls/fullchain.pem'), 'not a certificate');
  await rm(join(box.root, 'var/lib/o-ui/core/selection.json'));
  const damaged = box.run(['info']);
  assert.equal(damaged.status, 0, damaged.out);
  assert.match(damaged.out, /域名\/IP：\?；到期：\?/);
  assert.match(damaged.out, /Xray：未安装/);
  assert.match(damaged.out, /程序版本：v0\.1\.0/);
  assert.equal(box.run([], ['']).status, 0);
  const fresh = await sandbox();
  const prompt = fresh.run([], ['9']);
  assert.match(prompt.out, /请选择语言 \/ Select language/);
});

test('Xray自动安装失败（如断网）只警告，面板照常启用并启动', async () => {
  const box = await sandbox();
  const { status, out } = box.run(['install', '--package', box.pkg], ['2', '23456', '/x/', 'admin-core', '', ...acme(), 'n'], { FAKE_CORE_RC: '1' });
  assert.equal(status, 0, out);
  assert.match(out, /Xray could not be installed automatically/);
  assert.match(out, /Xray: not installed \(install it in the panel\)/);
  assert.match(out, /start at boot: enabled/);
  assert.ok(existsSync(join(box.state, 'active')) && existsSync(join(box.state, 'enabled')));
});

test('管理员密码须为6–72字节：5字节拒绝后重输，6字节接受', async () => {
  const box = await sandbox();
  const { status, out } = box.run(['install', '--package', box.pkg], ['1', '23456', '/x/', 'admin-pw', 'abcde', 'abcdef', 'abcdef', ...acme(), 'n']);
  assert.equal(status, 0, out);
  assert.match(out, /管理员密码（6–72字节/);
  assert.match(out, /长度须为6–72字节/);
  assert.equal(await box.log('admin-password'), 'abcdef');
  const long = await sandbox();
  const tooLong = long.run(['install', '--package', long.pkg], ['1', '23456', '/x/', 'admin-pw', 'x'.repeat(73), 'y'.repeat(72), 'y'.repeat(72), ...acme(), 'n']);
  assert.equal(tooLong.status, 0, tooLong.out);
  assert.equal(await long.log('admin-password'), 'y'.repeat(72));
});

test('非root、非Linux、不支持的架构、无systemd或已安装时拒绝', async () => {
  const box = await sandbox();
  assert.match(box.run(['install', '--package', box.pkg], [], { FAKE_UID: '1000' }).out, /请用root运行|run as root/);
  assert.match(box.run(['install', '--package', box.pkg], [], { FAKE_OS: 'Darwin' }).out, /仅支持Linux|only Linux/);
  assert.match(box.run(['install', '--package', box.pkg], [], { FAKE_ARCH: 'armv7l' }).out, /x86_64/);
  await rm(join(box.root, 'run/systemd'), { recursive: true });
  assert.match(box.run(['install', '--package', box.pkg]).out, /systemd/);
  await mkdir(join(box.root, 'run/systemd/system'), { recursive: true });
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n']).status, 0);
  const again = box.run(['install', '--package', box.pkg], ['1']);
  assert.notEqual(again.status, 0); assert.match(again.out, /已安装/);
});

test('网页升级助手：不接受参数，在固定名的临时服务里运行o-ui update，记录结果，防并发，卸载时删除', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n']).status, 0);
  const helper = join(box.root, 'usr/local/libexec/o-ui-update');
  assert.equal((await stat(helper)).mode & 0o777, 0o755);
  const update = (args: string[]) => { const child = spawnSync(helper, args, { encoding: 'utf8', env: { PATH: `${box.bin}:/usr/bin:/bin`, FAKE_STATE: box.state } }); return { status: child.status, out: child.stdout + child.stderr }; };
  for (const args of [['latest'], ['--run', 'x'], ['2026-10-21']]) {
    const result = update(args);
    assert.equal(result.status, 2, args.join(' ')); assert.match(result.out, /用法：o-ui-update/);
  }
  const started = update([]);
  assert.equal(started.status, 0, started.out); assert.match(started.out, /已开始升级/);
  assert.match(await box.log('calls.log'), new RegExp(`^systemd-run --unit=o-ui-update --collect --quiet --description=o-ui web update ${helper} --run$`, 'm'));
  await writeFile(join(box.state, 'update-active'), '');
  const busy = update([]);
  assert.equal(busy.status, 4); assert.match(busy.out, /升级已在进行/);
  await rm(join(box.state, 'update-active'));
  // 临时服务里的--run：运行已安装的o-ui update并写入结果（用命令桩代替真实升级）
  const cmd = join(box.root, 'usr/local/bin/o-ui');
  await writeFile(cmd, `#!/bin/sh\necho "upgrade $*"\nprintf '2026-10-21-v2\\n' >"${join(box.root, 'opt/o-ui/VERSION')}"\nexit "\${FAKE_UPDATE_RC:-0}"\n`, { mode: 0o755 });
  assert.equal(update(['--run']).status, 0);
  const done = JSON.parse(await box.read('var/lib/o-ui-update/status.json'));
  assert.equal(done.state, 'done'); assert.equal(done.from, 'v0.1.0'); assert.equal(done.to, '2026-10-21-v2'); assert.equal(done.code, 0);
  assert.equal(await box.read('var/lib/o-ui-update/output.log'), 'upgrade update\n');
  const failedRun = spawnSync(helper, ['--run'], { encoding: 'utf8', env: { PATH: `${box.bin}:/usr/bin:/bin`, FAKE_STATE: box.state, FAKE_UPDATE_RC: '3' } });
  assert.equal(failedRun.status, 3);
  const failed = JSON.parse(await box.read('var/lib/o-ui-update/status.json'));
  assert.equal(failed.state, 'failed'); assert.equal(failed.code, 3);
  assert.equal(box.run(['uninstall'], ['y', 'y', 'n'], { USE_SOURCE: '1' }).status, 0);
  assert.equal(existsSync(helper), false); assert.equal(existsSync(join(box.root, 'var/lib/o-ui-update')), false);
});

test('防火墙助手IP上限：严格校验，独立链挂在INPUT最前，封禁不重复，解封与清空只动本链', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n']).status, 0);
  for (const args of [['ban', '1.2.3', '50001', 'tcp'], ['ban', '1.2.3.256', '50001', 'tcp'], ['ban', '1.2.3.4;id', '50001', 'tcp'], ['ban', 'a.b.c.d', '50001', 'tcp'], ['ban', '2001:db8::g', '50001', 'tcp'], ['ban', '', '50001', 'tcp'], ['ban', '1.2.3.4/24', '50001', 'tcp'], ['ban', '1.2.3.4', '80', 'tcp'], ['ban', '1.2.3.4', '50001', 'icmp'], ['ban', '1.2.3.4', '50001'], ['unban', '1.2.3.4', '50001', 'tcp', 'x'], ['flush', 'x']]) {
    const result = box.helper(args);
    assert.equal(result.status, 2, args.join(' '));
    assert.match(result.out, /ban\|unban <IP>/);
  }
  assert.doesNotMatch(await box.log('calls.log'), /^ip6?tables /m, '参数无效时不调用iptables');
  assert.match(box.helper(['ban', '203.0.113.7', '50001', 'tcp']).out, /已封禁 203\.0\.113\.7 → 50001\/tcp/);
  assert.equal(box.helper(['ban', '203.0.113.7', '50001', 'tcp']).status, 0);
  assert.equal(await box.log('iptables-rules'), 'INPUT -j o-ui-iplimit\no-ui-iplimit -s 203.0.113.7 -p tcp --dport 50001 -j REJECT --reject-with tcp-reset\n', '重复封禁不重复加规则');
  assert.equal(box.helper(['ban', '2001:db8::1', '50002', 'udp']).status, 0);
  assert.equal(await box.log('ip6tables-rules'), 'INPUT -j o-ui-iplimit\no-ui-iplimit -s 2001:db8::1 -p udp --dport 50002 -j DROP\n');
  assert.match(box.helper(['unban', '203.0.113.7', '50001', 'tcp']).out, /已解封/);
  assert.equal(box.helper(['unban', '203.0.113.7', '50001', 'tcp']).status, 0, '重复解封无害');
  assert.equal(await box.log('iptables-rules'), 'INPUT -j o-ui-iplimit\n');
  assert.equal(box.helper(['flush']).status, 0);
  assert.equal(await box.log('ip6tables-rules'), 'INPUT -j o-ui-iplimit\n', 'flush只清空本链');
  await rename(join(box.bin, 'iptables'), join(box.bin, 'iptables.off'));
  const missing = box.helper(['ban', '203.0.113.8', '50001', 'tcp']);
  await rename(join(box.bin, 'iptables.off'), join(box.bin, 'iptables'));
  assert.equal(missing.status, 3); assert.match(missing.out, /未找到iptables，未封禁/);
  assert.equal(box.helper(['ban', '203.0.113.9', '50001', 'tcp']).status, 0);
  assert.equal(box.run(['uninstall'], ['y', 'y', 'n']).status, 0);
  assert.equal(await box.log('iptables-rules'), 'INPUT -j o-ui-iplimit\n', '卸载时清空IP上限封禁');
});

test('防火墙助手端口跳跃：严格校验，nat表独立链挂在PREROUTING，转发不重复，撤销与清空只动本链，卸载时清空', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n']).status, 0);
  for (const args of [['redirect', 'add', '48000', '50000'], ['redirect', 'add', '48000-48199-1', '50000'], ['redirect', 'add', '48199-48000', '50000'], ['redirect', 'add', '48000-48000', '50000'], ['redirect', 'add', '1000-1199', '50000'], ['redirect', 'add', '048000-48199', '50000'], ['redirect', 'add', '48000-48199', '48100'], ['redirect', 'add', '48000-48199', '80'], ['redirect', 'add', '48000-48199;id', '50000'], ['redirect', 'add', '48000-48199'], ['redirect', 'move', '48000-48199', '50000'], ['redirect', 'flush', 'x'], ['redirect']]) {
    const result = box.helper(args);
    assert.equal(result.status, 2, args.join(' '));
    assert.match(result.out, /redirect add\|delete/);
  }
  assert.doesNotMatch(await box.log('calls.log'), /^ip6?tables /m, '参数无效时不调用iptables');
  assert.match(box.helper(['redirect', 'add', '48000-48199', '50000']).out, /已设置端口跳跃 48000-48199\/udp → 50000 \//);
  assert.equal(box.helper(['redirect', 'add', '48000-48199', '50000']).status, 0);
  const rule = 'o-ui-hop -p udp --dport 48000:48199 -j REDIRECT --to-ports 50000';
  assert.equal(await box.log('iptables-nat-rules'), `PREROUTING -j o-ui-hop\n${rule}\n`, '重复设置不重复加规则');
  assert.equal(await box.log('ip6tables-nat-rules'), `PREROUTING -j o-ui-hop\n${rule}\n`);
  assert.equal(await box.log('iptables-rules'), '', '不改filter表');
  assert.equal(box.helper(['redirect', 'add', '52000-52199', '51000']).status, 0);
  assert.match(box.helper(['redirect', 'delete', '48000-48199', '50000']).out, /已撤销端口跳跃/);
  assert.equal(box.helper(['redirect', 'delete', '48000-48199', '50000']).status, 0, '重复撤销无害');
  assert.equal(await box.log('iptables-nat-rules'), 'PREROUTING -j o-ui-hop\no-ui-hop -p udp --dport 52000:52199 -j REDIRECT --to-ports 51000\n');
  assert.equal(box.helper(['redirect', 'flush']).status, 0);
  assert.equal(await box.log('iptables-nat-rules'), 'PREROUTING -j o-ui-hop\n', 'flush只清空本链');
  const v4only = box.helper(['redirect', 'add', '48000-48199', '50000'], { FAKE_NO_NAT6: '1' });
  assert.equal(v4only.status, 0); assert.match(v4only.out, /（仅IPv4）/);
  await rename(join(box.bin, 'iptables'), join(box.bin, 'iptables.off'));
  const missing = box.helper(['redirect', 'add', '52000-52199', '51000'], { FAKE_NO_NAT6: '1' });
  await rename(join(box.bin, 'iptables.off'), join(box.bin, 'iptables'));
  assert.equal(missing.status, 3); assert.match(missing.out, /未能设置端口跳跃转发/);
  assert.equal(box.run(['uninstall'], ['y', 'y', 'n']).status, 0);
  assert.equal(await box.log('iptables-nat-rules'), 'PREROUTING -j o-ui-hop\n', '卸载时清空端口跳跃转发');
});

const hostFirewall = 'table inet host_hardening {\n  chain input {\n    type filter hook input priority -50; policy drop;\n    iif "lo" accept\n    ct state established,related accept\n    ct state invalid drop\n    tcp dport 5522 accept\n    udp dport 443 accept\n    counter drop\n  }\n}\n';

async function nftSandbox() {
  const box = await sandbox();
  const result = box.run(['install', '--package', box.pkg], ['1', '23456', '', 'admin-test', '', ...acme(), 'n']);
  assert.equal(result.status, 0, result.out);
  await mkdir(join(box.root, 'etc/nftables.d'), { recursive: true });
  await writeFile(join(box.root, 'etc/nftables.d/host_hardening.nft'), hostFirewall, { mode: 0o640 });
  const initial = { nftables: [
    { chain: { family: 'inet', table: 'host_hardening', name: 'input', type: 'filter', hook: 'input', prio: -50, policy: 'drop' } },
    { rule: { family: 'inet', table: 'host_hardening', chain: 'input', handle: 16, expr: [{ match: { op: 'in', left: { ct: { key: 'state' } }, right: 'invalid' } }, { drop: null }] } },
    { rule: { family: 'inet', table: 'host_hardening', chain: 'input', handle: 18, expr: [{ counter: { packets: 10, bytes: 100 } }, { drop: null }] } },
  ] };
  await writeFile(join(box.state, 'nft.json'), JSON.stringify(initial));
  return { ...box, initial };
}

test('nft host_hardening：持久放行、重复保存、运行规则丢失后恢复及仅撤销自身规则', async () => {
  const box = await nftSandbox();
  const unit = await box.read('etc/systemd/system/o-ui.service');
  assert.match(unit, /^ReadWritePaths=-\/etc\/nftables.d$/m);
  for (const proto of ['udp', 'tcp']) {
    const result = box.helper(['allow', '50574', proto]);
    assert.equal(result.status, 0, result.out);
    assert.match(result.out, /nft host_hardening，已持久化/);
    assert.equal(box.helper(['allow', '50574', proto]).status, 0);
  }
  assert.equal(await box.read('var/lib/o-ui-firewall/rules'), 'nft 50574/udp\nnft 50574/tcp\n');
  assert.equal((await stat(join(box.root, 'var/lib/o-ui-firewall/rules'))).mode & 0o777, 0o600);
  assert.equal((await stat(join(box.root, 'etc/nftables.d/host_hardening.nft'))).mode & 0o777, 0o640);
  assert.equal((JSON.parse(await box.log('nft.json')).nftables.filter((e: any) => e.rule?.comment).length), 2);
  await writeFile(join(box.state, 'nft.json'), JSON.stringify(box.initial));
  assert.equal(box.helper(['allow', '50574', 'udp']).status, 0, '运行规则丢失时按持久配置重建');
  assert.equal(box.helper(['allow', '50574', 'tcp']).status, 0);
  assert.equal(box.helper(['delete', '50574', 'udp']).status, 0);
  assert.equal(box.helper(['delete', '50574', 'udp']).status, 0);
  assert.equal(box.helper(['delete', '50574', 'tcp']).status, 0);
  assert.equal(await box.read('etc/nftables.d/host_hardening.nft'), hostFirewall);
  assert.equal(await box.read('var/lib/o-ui-firewall/rules'), '');
  assert.deepEqual(JSON.parse(await box.log('nft.json')), box.initial, '原有drop及其计数不变');
  assert.doesNotMatch(await box.log('calls.log'), /nft (flush|delete table|--file \/etc)/);
});

test('nft自动放行：预检或提交失败时保留原文件，未知拦截层和不支持布局明确报错', async () => {
  const box = await nftSandbox();
  for (const fail of ['check', 'apply']) {
    const result = box.helper(['allow', '50574', 'udp'], { FAKE_NFT_FAIL: fail });
    assert.equal(result.status, 3, result.out);
    assert.equal(await box.read('etc/nftables.d/host_hardening.nft'), hostFirewall);
    assert.deepEqual(JSON.parse(await box.log('nft.json')), box.initial);
    if (existsSync(join(box.root, 'var/lib/o-ui-firewall/rules'))) assert.equal(await box.read('var/lib/o-ui-firewall/rules'), '');
  }
  const unknown = structuredClone(box.initial);
  unknown.nftables.push({ chain: { family: 'inet', table: 'other', name: 'input', type: 'filter', hook: 'input', prio: 10, policy: 'drop' } });
  await writeFile(join(box.state, 'nft.json'), JSON.stringify(unknown));
  const blocked = box.helper(['allow', '50574', 'udp']);
  assert.equal(blocked.status, 3); assert.match(blocked.out, /尚不支持的nftables INPUT拦截层/);
  await writeFile(join(box.state, 'nft.json'), JSON.stringify(box.initial));
  await writeFile(join(box.root, 'etc/nftables.d/host_hardening.nft'), hostFirewall.replace('counter drop', 'reject'));
  const layout = box.helper(['allow', '50574', 'udp']);
  assert.equal(layout.status, 3); assert.match(layout.out, /不支持的host_hardening持久化布局/);
});

test('nft与UFW同时存在须均放行，已有外部规则不接管、不删除', async () => {
  const box = await nftSandbox();
  const result = box.helper(['allow', '50574', 'udp'], { FAKE_UFW: '1' });
  assert.equal(result.status, 0, result.out);
  assert.equal(await box.read('var/lib/o-ui-firewall/rules'), 'nft 50574/udp\nufw 50574/udp\n');
  assert.match(await box.log('ufw-rules'), /50574\/udp ALLOW/);
  assert.equal(box.helper(['delete', '50574', 'udp'], { FAKE_UFW: '1' }).status, 0);
  const external = structuredClone(box.initial) as { nftables: any[] };
  external.nftables.splice(1, 0, { rule: { family: 'inet', table: 'host_hardening', chain: 'input', handle: 17, expr: [{ match: { op: '==', left: { payload: { protocol: 'udp', field: 'dport' } }, right: { set: [50574, { range: [51000, 51999] }] } } }, { accept: null }] } });
  await writeFile(join(box.state, 'nft.json'), JSON.stringify(external));
  assert.match(box.helper(['allow', '50574', 'udp']).out, /已有其他nft规则放行/);
  assert.match(box.helper(['delete', '50574', 'udp']).out, /不是o-ui添加/);
  assert.deepEqual(JSON.parse(await box.log('nft.json')), external);
  assert.equal(await box.read('etc/nftables.d/host_hardening.nft'), hostFirewall);
});

test('nft旧OUI共存：检查空链和纯计数返回链，不按链名放过阻断或未知跳转', async () => {
  const box = await nftSandbox();
  const legacy = structuredClone(box.initial) as { nftables: any[] };
  for (const family of ['ip', 'ip6']) {
    legacy.nftables.push(
      { chain: { family, table: 'filter', name: 'INPUT', type: 'filter', hook: 'input', policy: 'accept' } },
      { chain: { family, table: 'filter', name: 'f2b-3x-ipl' } },
      { rule: { family, table: 'filter', chain: 'INPUT', expr: [{ match: { op: '==', left: { meta: { key: 'l4proto' } }, right: 'tcp' } }, { counter: { packets: 12, bytes: 1200 } }, { jump: { target: 'f2b-3x-ipl' } }] } },
    );
  }
  legacy.nftables.push(
    { chain: { family: 'ip', table: 'filter', name: 'oui-ipacct-in' } },
    { rule: { family: 'ip', table: 'filter', chain: 'INPUT', expr: [{ counter: { packets: 1, bytes: 100 } }, { jump: { target: 'oui-ipacct-in' } }] } },
  );
  await writeFile(join(box.state, 'nft.json'), JSON.stringify(legacy));
  for (const proto of ['tcp', 'udp']) {
    const result = box.helper(['allow', '50575', proto]);
    assert.equal(result.status, 0, result.out);
    assert.equal(box.helper(['delete', '50575', proto]).status, 0);
  }
  assert.deepEqual(JSON.parse(await box.log('nft.json')), legacy, '旧链、跳转和计数器不变');
  const accounting = structuredClone(legacy);
  accounting.nftables.push({ rule: { family: 'ip', table: 'filter', chain: 'oui-ipacct-in', expr: [{ match: { op: '==', left: { payload: { protocol: 'tcp', field: 'dport' } }, right: 50575 } }, { counter: { packets: 3, bytes: 300 } }, { return: null }] } });
  await writeFile(join(box.state, 'nft.json'), JSON.stringify(accounting));
  assert.equal(box.helper(['allow', '50575', 'udp']).status, 0, '纯计数/返回链可共存');
  assert.equal(box.helper(['delete', '50575', 'udp']).status, 0);
  assert.deepEqual(JSON.parse(await box.log('nft.json')), accounting);
  for (const expr of [[{ drop: null }], [{ reject: null }], [{ jump: { target: 'unknown' } }], [{ goto: { target: 'unknown' } }], [{ queue: null }], [{ mangle: { key: { meta: { key: 'mark' } }, value: 1 } }]]) {
    const blocked = structuredClone(legacy);
    blocked.nftables.push({ rule: { family: 'ip', table: 'filter', chain: 'f2b-3x-ipl', expr } });
    await writeFile(join(box.state, 'nft.json'), JSON.stringify(blocked));
    const result = box.helper(['allow', '50575', 'udp']);
    assert.equal(result.status, 3, result.out);
    assert.match(result.out, /尚不支持的nftables INPUT拦截层/);
    assert.deepEqual(JSON.parse(await box.log('nft.json')), blocked);
    assert.equal(await box.read('etc/nftables.d/host_hardening.nft'), hostFirewall);
  }
  const missing = structuredClone(legacy);
  missing.nftables = missing.nftables.filter((e: any) => e.chain?.name !== 'f2b-3x-ipl');
  await writeFile(join(box.state, 'nft.json'), JSON.stringify(missing));
  assert.equal(box.helper(['allow', '50575', 'udp']).status, 3, '目标缺失仍拒绝');
});

test('nft安全边界：拒绝提前阻断、跳转、外部改写的自有规则与可写配置', async () => {
  const box = await nftSandbox();
  const limit = structuredClone(box.initial) as { nftables: any[] };
  limit.nftables.push({ chain: { family: 'ip', table: 'filter', name: 'INPUT', type: 'filter', hook: 'input', policy: 'accept' } }, { rule: { family: 'ip', table: 'filter', chain: 'INPUT', expr: [{ jump: { target: 'o-ui-iplimit' } }] } });
  await writeFile(join(box.state, 'nft.json'), JSON.stringify(limit));
  assert.equal(box.helper(['allow', '50575', 'udp']).status, 0, '兼容助手自身IP上限链');
  assert.equal(box.helper(['delete', '50575', 'udp']).status, 0);
  for (const expr of [[{ jump: { target: 'other' } }], [{ match: { op: '==', left: { payload: { protocol: 'udp', field: 'dport' } }, right: 50574 } }, { drop: null }]]) {
    const changed = structuredClone(box.initial) as { nftables: any[] };
    changed.nftables.splice(1, 0, { rule: { family: 'inet', table: 'host_hardening', chain: 'input', handle: 17, expr } });
    await writeFile(join(box.state, 'nft.json'), JSON.stringify(changed));
    const result = box.helper(['allow', '50574', 'udp']);
    assert.equal(result.status, 3, result.out); assert.match(result.out, /额外拦截/);
    assert.equal(await box.read('etc/nftables.d/host_hardening.nft'), hostFirewall);
  }
  await writeFile(join(box.state, 'nft.json'), JSON.stringify(box.initial));
  await chmod(join(box.root, 'etc/nftables.d/host_hardening.nft'), 0o666);
  assert.equal(box.helper(['allow', '50574', 'udp']).status, 3);
  await chmod(join(box.root, 'etc/nftables.d/host_hardening.nft'), 0o640);
  assert.equal(box.helper(['allow', '50574', 'udp']).status, 0);
  const changed = JSON.parse(await box.log('nft.json'));
  changed.nftables.find((entry: any) => entry.rule?.comment).rule.expr[0].match.right = 50575;
  await writeFile(join(box.state, 'nft.json'), JSON.stringify(changed));
  assert.equal(box.helper(['delete', '50574', 'udp']).status, 3);
  assert.deepEqual(JSON.parse(await box.log('nft.json')), changed, '不能删除已被外部改写的标记规则');
});

test('防火墙助手：严格校验参数，只撤销自己加过的规则，ufw与firewalld及无防火墙', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n']).status, 0);
  for (const args of [['allow', '80', 'tcp'], ['allow', '65536', 'tcp'], ['allow', '01024', 'tcp'], ['allow', '2000', 'icmp'], ['open', '2000', 'tcp'], ['allow', '2000'], ['allow', '2000', 'tcp', 'x'], ['allow', '2000;id', 'tcp'], ['allow', '-1', 'udp'], ['delete', '', 'tcp']]) {
    const result = box.helper(args, { FAKE_UFW: '1' });
    assert.equal(result.status, 2, args.join(' '));
    assert.match(result.out, /用法：o-ui-firewall allow\|delete/);
  }
  assert.doesNotMatch(await box.log('calls.log'), /^ufw (allow|delete) [^2]/m);
  const none = box.helper(['allow', '50000', 'udp']);
  assert.equal(none.status, 4); assert.match(none.out, /未检测到启用的UFW\/firewalld或受支持的nftables拦截层/);
  // ufw：已有的他人规则（如x-ui的52890）不记录、不删除；自己加的只加一次，撤销后清除记录
  await writeFile(join(box.state, 'ufw-rules'), '52890/tcp ALLOW Anywhere\n');
  const env = { FAKE_UFW: '1' };
  assert.match(box.helper(['allow', '52890', 'tcp'], env).out, /52890\/tcp 已有其他规则放行，未改动/);
  assert.match(box.helper(['delete', '52890', 'tcp'], env).out, /52890\/tcp 不是o-ui添加的规则，未改动 \/ 52890\/tcp was not added by o-ui; unchanged/);
  assert.match(box.helper(['allow', '50001', 'tcp'], env).out, /^已放行 50001\/tcp（ufw） \/ allowed 50001\/tcp \(ufw\)\n$/);
  assert.match(box.helper(['allow', '50001', 'tcp'], env).out, /50001\/tcp 已放行（ufw）/);
  assert.equal(((await box.log('calls.log')).match(/^ufw allow 50001\/tcp comment o-ui inbound$/gm) ?? []).length, 1);
  assert.equal(await box.read('var/lib/o-ui-firewall/rules'), 'ufw 50001/tcp\n');
  assert.equal((await stat(join(box.root, 'var/lib/o-ui-firewall/rules'))).mode & 0o777, 0o600);
  assert.match(box.helper(['delete', '50001', 'tcp'], env).out, /^已撤销 50001\/tcp（ufw） \/ removed 50001\/tcp \(ufw\)\n$/);
  assert.equal(await box.read('var/lib/o-ui-firewall/rules'), '');
  assert.equal(await box.log('ufw-rules'), '52890/tcp ALLOW Anywhere\n');
  assert.doesNotMatch(await box.log('calls.log'), /delete allow 52890/);
  // firewalld：永久放行后reload，撤销同样只限记录
  const fw = { FAKE_FIREWALLD: '1' };
  assert.match(box.helper(['allow', '50002', 'udp'], fw).out, /已放行 50002\/udp（firewalld）/);
  const calls = await box.log('calls.log');
  assert.ok(calls.indexOf('firewall-cmd --permanent --add-port=50002/udp') < calls.lastIndexOf('firewall-cmd --reload'));
  assert.match(box.helper(['delete', '50002', 'udp'], fw).out, /已撤销 50002\/udp（firewalld）/);
  assert.match(await box.log('calls.log'), /^firewall-cmd --permanent --remove-port=50002\/udp$/m);
  assert.equal(await box.log('firewalld-ports'), '');
});

test('没有sudo时询问安装，安装失败或拒绝只提示手动放行；visudo校验失败不写sudoers', async () => {
  const box = await sandbox();
  await rm(join(box.bin, 'visudo'));
  const { status, out } = box.run(['install', '--package', box.pkg], [...start(), ...acme(), 'n', 'y']);
  assert.equal(status, 0, out);
  assert.match(out, /未找到sudo；面板创建入站时要用它放行防火墙端口/);
  assert.match(out, /未安装sudo：面板创建或删除入站时不能自动放行或撤销防火墙端口，请手动处理/);
  assert.ok(existsSync(join(box.root, 'usr/local/libexec/o-ui-firewall')));
  assert.equal(existsSync(join(box.root, 'etc/sudoers.d/o-ui')), false);
  const bad = await sandbox();
  const rejected = bad.run(['install', '--package', bad.pkg], [...start(), ...acme(), 'n'], { FAKE_VISUDO_FAIL: '1' });
  assert.equal(rejected.status, 0, rejected.out);
  assert.match(rejected.out, /sudoers校验未通过，未安装/);
  assert.equal(existsSync(join(bad.root, 'etc/sudoers.d/o-ui')), false);
  assert.deepEqual((await readdir(join(bad.root, 'etc/sudoers.d'))).filter(name => name.startsWith('.')), []);
});
