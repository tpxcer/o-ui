import test from 'node:test';
import assert from 'node:assert/strict';
import { chmod, mkdir, mkdtemp, readdir, readFile, rename, stat, writeFile, rm } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync, spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { packBundle } from '../scripts/build-release.mjs';

// install.sh只在临时根目录内运行，systemctl/runuser/acme.sh/sysctl等系统命令全部由桩记录；不触碰本机系统。
// 证书扫描使用真实openssl生成的临时测试证书。
const installer = fileURLToPath(new URL('../install.sh', import.meta.url));
const startScript = fileURLToPath(new URL('../scripts/start-local.sh', import.meta.url));
const sha = (data: Buffer | string) => createHash('sha256').update(data).digest('hex');

// iptables/ip6tables命令桩：链与规则按“链 参数”记在状态目录，-C按整行匹配
const IPTABLES_STUB = `s=$FAKE_STATE; t=$(basename "$0")
echo "$t $*" >>"$s/calls.log"
[ "$1" = -w ] && shift
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
  journalctl: `echo "journalctl $*" >>"$FAKE_STATE/calls.log"`,
  runuser: `echo "runuser LC_ALL=$LC_ALL $*" >>"$FAKE_STATE/calls.log"
[ "$1" = -u ] && [ "$2" = o-ui ] && [ "$3" = -- ] || exit 97
shift 3
exec "$@"`,
  systemctl: `s=$FAKE_STATE
echo "systemctl $*" >>"$s/calls.log"
unit="$O_UI_ROOT_PREFIX/etc/systemd/system/o-ui.service"
case "$1" in
  is-active) [ -f "$s/active" ]; exit ;;
  is-enabled) [ -f "$s/enabled" ]; exit ;;
  enable) [ "$2" = o-ui ] && : >"$s/enabled"; exit 0 ;;
  start|restart)
    [ "$2" = o-ui ] || exit 0
    if [ -n "\${FAKE_FAIL_VERSION:-}" ] && [ "$(cat "$O_UI_ROOT_PREFIX/opt/o-ui/VERSION")" = "$FAKE_FAIL_VERSION" ]; then rm -f "$s/active" "$s/listening"; exit 0; fi
    : >"$s/active"; awk '/^ExecStart=/ { print $NF }' "$unit" >"$s/listening"; exit 0 ;;
  stop) [ "$2" = o-ui ] && rm -f "$s/active" "$s/listening"; exit 0 ;;
  disable) case " $* " in *" o-ui "*) rm -f "$s/active" "$s/listening" "$s/enabled" ;; esac; exit 0 ;;
  status) echo "o-ui.service fake status"; exit 0 ;;
esac
exit 0`,
  ss: `echo 'State Recv-Q Send-Q Local Address:Port Peer Address:Port'
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
  status) if [ -n "\${FAKE_UFW:-}" ]; then echo 'Status: active'; echo; echo 'To Action From'; cat "$s/ufw-rules" 2>/dev/null || true; else echo 'Status: inactive'; fi ;;
  allow) echo "ufw $*" >>"$s/calls.log"; echo "$2 ALLOW Anywhere" >>"$s/ufw-rules" ;;
  delete) echo "ufw $*" >>"$s/calls.log"; grep -v "^$3 " "$s/ufw-rules" >"$s/ufw-rules.tmp" || true; mv "$s/ufw-rules.tmp" "$s/ufw-rules" ;;
  *) echo "ufw $*" >>"$s/calls.log" ;;
esac`,
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
  sudo: `echo "sudo $*" >>"$FAKE_STATE/calls.log"`,
  visudo: `echo "visudo $*" >>"$FAKE_STATE/calls.log"
[ -z "\${FAKE_VISUDO_FAIL:-}" ] && grep -qx 'o-ui ALL=(root) NOPASSWD: /usr/local/libexec/o-ui-firewall' "$2"`,
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
mode=''; key=''; full=''
while [ $# -gt 0 ]; do case $1 in --issue) mode=issue ;; --install-cert) mode=install ;; --renew) mode=renew ;; --remove) mode=remove ;; --key-file) key=$2; shift ;; --fullchain-file) full=$2; shift ;; esac; shift; done
case $mode in
  issue) exit "\${FAKE_ACME_ISSUE_RC:-0}" ;;
  install) if [ -n "\${FAKE_ACME_CERT:-}" ]; then cp "$FAKE_ACME_CERT" "$full"; cp "$FAKE_ACME_KEY" "$key"; else printf 'CERT\\n' >"$full"; printf 'KEY\\n' >"$key"; fi ;;
  renew) exit "\${FAKE_ACME_RENEW_RC:-0}" ;;
esac
exit 0
`;

// 仅测试用的已过期证书与私钥（2020-01-01至2020-01-02），用于验证扫描会排除过期证书
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
  await writeFile(join(root, 'proc/sys/net/core/default_qdisc'), 'pfifo_fast\n');
  await writeFile(join(root, 'proc/sys/net/ipv4/tcp_congestion_control'), 'cubic\n');
  await writeFile(join(root, 'proc/sys/net/ipv4/tcp_available_congestion_control'), 'reno cubic bbr\n');
  if (options.acme !== false) {
    await mkdir(join(root, 'etc/o-ui/acme'), { recursive: true });
    await writeFile(join(root, 'etc/o-ui/acme/acme.sh'), fakeAcme, { mode: 0o755 });
  }
  const issued = await makeCert(join(base, 'issued'), 'fullchain.pem', 'key.pem', ['example.com']);
  const pkg = await makePackage(downloads);
  const run = (args: string[], answers: string[] = [], env: Record<string, string> = {}) => {
    const script = existsSync(join(root, 'usr/local/bin/o-ui')) && !env.USE_SOURCE ? join(root, 'usr/local/bin/o-ui') : installer;
    const child = spawnSync('bash', [script, ...args], {
      input: answers.map(line => line + '\n').join(''), encoding: 'utf8',
      env: { PATH: `${bin}:/usr/bin:/bin`, HOME: base, LANG: 'C', O_UI_ROOT_PREFIX: root, O_UI_STDIN_ANSWERS: '1', FAKE_STATE: state, FAKE_DOWNLOADS: downloads, FAKE_ACME_CERT: issued.cert, FAKE_ACME_KEY: issued.key, ...env },
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
  return { base, root, bin, state, downloads, pkg, run, read, log, helper };
}

const conf = async (box: Awaited<ReturnType<typeof sandbox>>) => Object.fromEntries((await box.read('etc/o-ui/o-ui.conf')).trim().split('\n').map(line => line.split('=', 2)));
// 常用问答：语言、端口、访问路径、用户名、密码（回车随机）
const start = (port = '23456', path = '/panelqa/') => ['1', port, path, 'admin-test', ''];
// ACME：域名、HTTP-01、邮箱留空、手动续期
const acme = (host = 'example.com') => ['1', host, '1', '', '2'];

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
  const answers = ['1', '23456', '', 'admin-test', '', '1', 'Example.COM', '1', '', '1', 'y', 'y'];
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
  assert.equal(await box.read('etc/sudoers.d/o-ui'), '# o-ui: allow the panel to run its firewall helper as root (the helper validates every argument)\no-ui ALL=(root) NOPASSWD: /usr/local/libexec/o-ui-firewall\n');
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
  assert.ok(acmeLog.includes('[--reloadcmd] [/usr/local/bin/o-ui cert-reload]'));
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

test('英文安装：自定义访问路径、Cloudflare DNS验证、手动续期，令牌只经环境变量传给acme.sh', async () => {
  const box = await sandbox();
  const answers = ['2', '30001', 'myPanel', 'ops_admin', 'correct-horse-battery', 'correct-horse-battery', '1', 'panel.example.org', '2', 'cf-secret-token-123', 'zone123', 'ops@example.org', '2', 'n'];
  const { status, out } = box.run(['install', '--package', box.pkg], answers);
  assert.equal(status, 0, out);
  const acmeLog = await box.log('acme.log'), unit = await box.read('etc/systemd/system/o-ui.service');
  assert.match(acmeLog, /\[--dns\] \[dns_cf\] CF_Token=set CF_Zone_ID=zone123/);
  assert.doesNotMatch(acmeLog + (await box.log('calls.log')) + out, /cf-secret-token-123/);
  assert.ok(unit.includes('Environment=LANG=en_US.UTF-8\n') && unit.includes('Environment=O_UI_DEFAULT_LANG=en\n'));
  assert.equal(await box.log('panel-path'), '/myPanel/');
  assert.equal(await box.log('admin-password'), 'correct-horse-battery');
  assert.ok(!out.includes('correct-horse-battery'), '用户输入的密码不回显');
  assert.match(out, /URL: https:\/\/panel\.example\.org:30001\/myPanel\//);
  assert.match(out, /Password: stored hashed and cannot be shown/);
  assert.match(out, /Certificate: Let's Encrypt \(manual renewal\)/);
  assert.doesNotMatch(out.slice(out.indexOf('2) English') + 10), /[一-鿿]/u, '选英文后不再出现中文提示');
});

test('IP证书只用HTTP-01并选择短期证书，按3天续期', async () => {
  const box = await sandbox();
  const { status, out } = box.run(['install', '--package', box.pkg], [...start('24000', '/'), '1', '203.0.113.10', '', '1', 'n']);
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

test('已有证书：扫描常见位置只列有效配对，编号选择后自动写入面板', async () => {
  const box = await sandbox();
  await makeCert(join(box.root, 'root/cert/files.example.com'), 'fullchain.pem', 'privkey.pem', ['files.example.com', 'www.files.example.com']);
  await makeCert(join(box.root, 'root/.acme.sh/acme.example.com_ecc'), 'fullchain.cer', 'acme.example.com.key', ['acme.example.com']);
  await makeCert(join(box.root, 'etc/letsencrypt/live/le.example.com'), 'fullchain.pem', 'privkey.pem', ['le.example.com']);
  await mkdir(join(box.root, 'root/cert/expired.example.com'), { recursive: true });
  await writeFile(join(box.root, 'root/cert/expired.example.com/fullchain.pem'), expiredCert);
  await writeFile(join(box.root, 'root/cert/expired.example.com/privkey.pem'), expiredKey);
  const a = await makeCert(join(box.base, 'mix-a'), 'c.pem', 'k.pem', ['mismatch.example.com']), b = await makeCert(join(box.base, 'mix-b'), 'c.pem', 'k.pem', ['other.example.com']);
  await mkdir(join(box.root, 'root/cert/mismatch.example.com'), { recursive: true });
  await writeFile(join(box.root, 'root/cert/mismatch.example.com/fullchain.pem'), await readFile(a.cert));
  await writeFile(join(box.root, 'root/cert/mismatch.example.com/privkey.pem'), await readFile(b.key));
  await makeCert(join(box.root, 'root/cert/nosan.example.com'), 'fullchain.pem', 'privkey.pem', []);
  const { status, out } = box.run(['install', '--package', box.pkg], [...start('27000'), '2', '3', 'n']);
  assert.equal(status, 0, out);
  const from = out.indexOf('找到以下有效证书'), listing = out.slice(from, out.indexOf('请输入序号', from));
  assert.equal((listing.match(/^ {2}\d\) /gm) ?? []).length, 3, listing);
  for (const host of ['files.example.com,www.files.example.com', 'acme.example.com', 'le.example.com']) assert.ok(listing.includes(host), host);
  for (const hidden of ['expired.example.com', 'mismatch.example.com', 'nosan.example.com']) assert.ok(!listing.includes(hidden), hidden);
  // 扫描顺序：/etc/o-ui/tls、/root/cert、/root/.acme.sh、/etc/letsencrypt/live
  assert.match(listing, /  1\) files\.example\.com[\s\S]*  2\) acme\.example\.com[\s\S]*  3\) le\.example\.com .*\/etc\/letsencrypt\/live\/le\.example\.com\/fullchain\.pem/);
  assert.equal(await box.read('etc/o-ui/tls/fullchain.pem'), await box.read('etc/letsencrypt/live/le.example.com/fullchain.pem'));
  assert.equal((await stat(join(box.root, 'etc/o-ui/tls/key.pem'))).mode & 0o777, 0o640);
  assert.match(await box.log('oui.log'), /\[-panel-origin\] \[https:\/\/le\.example\.com:27000\]/);
  assert.equal(await box.log('acme.log'), '');
  assert.equal((await conf(box)).OUI_MODE, 'file');
  assert.match(out, /证书：已有证书；域名\/IP：le\.example\.com；到期：/);
});

test('已有证书含多个名称时编号选择面板域名', async () => {
  const box = await sandbox();
  await makeCert(join(box.root, 'root/cert/files.example.com'), 'fullchain.pem', 'privkey.pem', ['files.example.com', 'www.files.example.com']);
  const { status, out } = box.run(['install', '--package', box.pkg], [...start('27001'), '2', '1', '2', 'n']);
  assert.equal(status, 0, out);
  assert.match(await box.log('oui.log'), /\[-panel-origin\] \[https:\/\/www\.files\.example\.com:27001\]/);
});

test('没有可用的已有证书时自动改为申请证书；80端口被占用时不使用HTTP-01', async () => {
  const box = await sandbox();
  await writeFile(join(box.state, 'listening-extra'), '80\n');
  const { status, out } = box.run(['install', '--package', box.pkg], [...start('25000'), '2', 'example.com', '1', '1', 'example.com', '2', 'token-x', '', '', '2', 'n']);
  assert.equal(status, 0, out);
  assert.match(out, /未在 \/root\/cert、\/root\/\.acme\.sh、\/etc\/letsencrypt\/live、\/etc\/o-ui 找到有效的证书与私钥配对/);
  assert.match(out, /80端口已被占用，HTTP-01无法使用；请改用Cloudflare DNS或已有证书/);
  assert.match(await box.log('acme.log'), /\[--dns\] \[dns_cf\]/);
  assert.equal((await conf(box)).OUI_MODE, 'acme');
});

test('证书申请失败可重新选择方式；放弃时只以本机启动，不开放HTTP', async () => {
  const retry = await sandbox();
  await makeCert(join(retry.root, 'root/cert/files.example.com'), 'fullchain.pem', 'privkey.pem', ['files.example.com']);
  const ok = retry.run(['install', '--package', retry.pkg], [...start('26000'), ...acme(), 'n', 'y', '2', '1'], { FAKE_ACME_ISSUE_RC: '1' });
  assert.equal(ok.status, 0, ok.out);
  assert.match(ok.out, /HTTPS证书未配置成功，是否重新选择证书方式并重试/);
  assert.match(await retry.log('acme.log'), /\[--remove\] \[-d\] \[example\.com\] \[--ecc\]/);
  assert.match(await retry.log('oui.log'), /\[-panel-origin\] \[https:\/\/files\.example\.com:26000\]/);
  assert.equal((await conf(retry)).OUI_MODE, 'file');
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
  const { status, out } = box.run(['install', '--package', box.pkg], [...start(), '1', 'example.com', '1', '', '1', 'n']);
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
  const installed = box.run(['install', '--package', box.pkg], [...start(), '1', 'example.com', '1', '', '1', 'y', 'y'], { FAKE_UFW: '1' });
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
  const again = box.run(['install', '--package', box.pkg], ['1', '23456', '/panelqa/', '2', '1'], { USE_SOURCE: '1' });
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

test('o-ui cert：由申请证书改为扫描到的已有证书，先停面板再离线写入并停用旧ACME条目', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], [...start(), '1', 'example.com', '1', '', '1', 'n']).status, 0);
  await makeCert(join(box.root, 'root/cert/later.example.com'), 'fullchain.pem', 'privkey.pem', ['later.example.com']);
  const { status, out } = box.run(['cert'], ['2', '2']);
  assert.equal(status, 0, out);
  const calls = await box.log('calls.log'), https = calls.lastIndexOf('-enable-panel-https');
  assert.ok(calls.lastIndexOf('systemctl stop o-ui', https) > 0 && calls.indexOf('systemctl start o-ui', https) > https);
  assert.match(await box.log('acme.log'), /\[--remove\] \[-d\] \[example\.com\] \[--ecc\]/);
  assert.equal(existsSync(join(box.root, 'etc/systemd/system/o-ui-cert-renew.timer')), false);
  assert.match(out, /https:\/\/later\.example\.com:23456\/panelqa\//);
  assert.equal((await conf(box)).OUI_MODE, 'file');
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
  for (const line of [/访问地址：https:\/\/example\.com:23456\/panelqa\//, /用户名：admin-test/, /证书：Let's Encrypt（手动续期）；域名\/IP：example\.com/, /Xray：26\.3\.27/, /服务：运行中；开机自启：已启用/, /BBR/]) assert.match(info.out, line);
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
  assert.equal(none.status, 0); assert.match(none.out, /^无防火墙，未改动 \/ no firewall, nothing changed\n$/);
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
