import test from 'node:test';
import assert from 'node:assert/strict';
import { chmod, mkdir, mkdtemp, readFile, stat, writeFile, rm } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync, spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { packBundle } from '../scripts/build-release.mjs';

// install.sh只在临时根目录内运行，systemctl/runuser/acme.sh/sysctl等系统命令全部由桩记录；不触碰本机系统。
const installer = fileURLToPath(new URL('../install.sh', import.meta.url));
const startScript = fileURLToPath(new URL('../scripts/start-local.sh', import.meta.url));
const sha = (data: Buffer | string) => createHash('sha256').update(data).digest('hex');

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
  start|restart)
    [ "$2" = o-ui ] || exit 0
    if [ -n "\${FAKE_FAIL_VERSION:-}" ] && [ "$(cat "$O_UI_ROOT_PREFIX/opt/o-ui/VERSION")" = "$FAKE_FAIL_VERSION" ]; then rm -f "$s/active" "$s/listening"; exit 0; fi
    : >"$s/active"; awk '/^ExecStart=/ { print $NF }' "$unit" >"$s/listening"; exit 0 ;;
  stop) [ "$2" = o-ui ] && rm -f "$s/active" "$s/listening"; exit 0 ;;
  disable) case " $* " in *" o-ui "*) rm -f "$s/active" "$s/listening" ;; esac; exit 0 ;;
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
  ufw: `case $1 in status) [ -n "\${FAKE_UFW:-}" ] && echo 'Status: active' || echo 'Status: inactive' ;; *) echo "ufw $*" >>"$FAKE_STATE/calls.log" ;; esac`,
  curl: `out=''; url=''
while [ $# -gt 0 ]; do case $1 in -o) out=$2; shift ;; -*) ;; *) url=$1 ;; esac; shift; done
echo "curl $url" >>"$FAKE_STATE/calls.log"
cp "$FAKE_DOWNLOADS/$(basename "$url")" "$out" 2>/dev/null || exit 22`,
};

// 假o-ui程序：记录参数与标准输入，模拟离线CLI写出的结果
const fakePanel = `#!/bin/sh
s=$FAKE_STATE
{ printf 'argv'; for a in "$@"; do printf ' [%s]' "$a"; done; printf ' LC_ALL=%s\\n' "$LC_ALL"; } >>"$s/oui.log"
state=''; cert=''; key=''
cmd=$1
while [ $# -gt 0 ]; do case $1 in -state) state=$2; shift ;; -panel-cert) cert=$2; shift ;; -panel-key) key=$2; shift ;; esac; shift; done
case $cmd in
  -init-panel-admin)
    IFS= read -r pw || exit 9
    printf '%s' "$pw" >"$s/admin-password"
    mkdir -p "$state"; : >"$state/panel.sqlite"
    exit "\${FAKE_INIT_RC:-0}" ;;
  -enable-panel-https)
    [ -z "\${FAKE_HTTPS_RC:-}" ] || exit "$FAKE_HTTPS_RC"
    [ -r "$cert" ] && [ -r "$key" ] || exit 8
    : >"$s/https-enabled"; exit 0 ;;
  -recover-panel-access) : >"$s/local-restored"; exit 0 ;;
esac
exit 0
`;

const fakeAcme = `#!/bin/sh
{ printf 'acme'; for a in "$@"; do printf ' [%s]' "$a"; done; printf ' CF_Token=%s CF_Zone_ID=%s\\n' "\${CF_Token:+set}" "\${CF_Zone_ID:-}"; } >>"$FAKE_STATE/acme.log"
mode=''; key=''; full=''
while [ $# -gt 0 ]; do case $1 in --issue) mode=issue ;; --install-cert) mode=install ;; --renew) mode=renew ;; --remove) mode=remove ;; --key-file) key=$2; shift ;; --fullchain-file) full=$2; shift ;; esac; shift; done
case $mode in
  issue) exit "\${FAKE_ACME_ISSUE_RC:-0}" ;;
  install) printf 'CERT\\n' >"$full"; printf 'KEY\\n' >"$key" ;;
  renew) exit "\${FAKE_ACME_RENEW_RC:-0}" ;;
esac
exit 0
`;

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
  const pkg = await makePackage(downloads);
  const run = (args: string[], answers: string[] = [], env: Record<string, string> = {}) => {
    const script = existsSync(join(root, 'usr/local/bin/o-ui')) && !env.USE_SOURCE ? join(root, 'usr/local/bin/o-ui') : installer;
    const child = spawnSync('bash', [script, ...args], {
      input: answers.map(line => line + '\n').join(''), encoding: 'utf8',
      env: { PATH: `${bin}:/usr/bin:/bin:/usr/sbin:/sbin`, HOME: base, LANG: 'C', O_UI_ROOT_PREFIX: root, O_UI_STDIN_ANSWERS: '1', FAKE_STATE: state, FAKE_DOWNLOADS: downloads, ...env },
    });
    return { status: child.status, out: child.stdout + child.stderr };
  };
  const read = (path: string) => readFile(join(root, path), 'utf8');
  const log = async (name: string) => existsSync(join(state, name)) ? readFile(join(state, name), 'utf8') : '';
  return { base, root, state, downloads, pkg, run, read, log };
}

const conf = async (box: Awaited<ReturnType<typeof sandbox>>) => Object.fromEntries((await box.read('etc/o-ui/o-ui.conf')).trim().split('\n').map(line => line.split('=', 2)));

test('脚本可被bash解析，且以main调用收尾避免下载不完整时执行半截脚本', async () => {
  execFileSync('bash', ['-n', installer]);
  const text = await readFile(installer, 'utf8');
  assert.match(text.trimEnd(), /\nmain "\$@"\nexit \$\?$/);
  assert.match(text, /^set -euo pipefail$/m);
  assert.doesNotMatch(text, /curl[^\n]*\|\s*(ba)?sh/, '不得管道执行远程脚本');
  const shellcheck = spawnSync('shellcheck', ['--version']);
  if (shellcheck.status === 0) execFileSync('shellcheck', ['-s', 'bash', installer]);
});

test('全新安装：中文、HTTP-01域名证书、自动续期、BBR与UFW放行，HTTPS首登', async () => {
  const box = await sandbox();
  const answers = ['1', '23456', 'admin-test', '', '1', 'Example.COM', '1', '', '1', 'y', 'y'];
  const { status, out } = box.run(['install', '--package', box.pkg], answers, { FAKE_UFW: '1' });
  assert.equal(status, 0, out);
  // 发布包与管理命令
  assert.equal(await box.read('usr/local/bin/o-ui'), await readFile(installer, 'utf8'));
  assert.equal((await stat(join(box.root, 'opt/o-ui/bin/o-ui'))).mode & 0o777, 0o755);
  assert.equal((await stat(join(box.root, 'opt/o-ui/dist/index.html'))).mode & 0o777, 0o644);
  assert.equal((await stat(join(box.root, 'var/lib/o-ui'))).mode & 0o777, 0o700);
  // systemd单元：非root、SIGTERM只给主进程、失败才重启
  const unit = await box.read('etc/systemd/system/o-ui.service');
  for (const line of ['User=o-ui', 'Group=o-ui', 'ExecStart=/opt/o-ui/o-ui /var/lib/o-ui 23456', 'Restart=on-failure', 'KillMode=mixed', 'KillSignal=SIGTERM', 'Environment=LANG=zh_CN.UTF-8', 'Environment=O_UI_DEFAULT_LANG=zh', 'NoNewPrivileges=yes']) assert.ok(unit.includes(line + '\n'), line);
  assert.match(await box.read('etc/systemd/system/o-ui-cert-renew.timer'), /OnCalendar=\*-\*-\* 00\/6:17:00/);
  assert.match(await box.read('etc/systemd/system/o-ui-cert-renew.service'), /ExecStart=\/etc\/o-ui\/acme\/acme\.sh --cron --home \/etc\/o-ui\/acme /);
  // 管理员离线初始化：服务用户执行、密码只经标准输入
  const calls = await box.log('calls.log'), panel = await box.log('oui.log'), password = await box.log('admin-password');
  assert.match(password, /^[A-Za-z0-9]{24}$/);
  assert.ok(!panel.includes(password) && !calls.includes(password), '密码不得出现在命令行参数中');
  assert.ok(panel.includes(`argv [-init-panel-admin] [-state] [${box.root}/var/lib/o-ui/panel] [-username] [admin-test] LC_ALL=zh_CN.UTF-8`), panel);
  assert.ok(panel.includes(`argv [-enable-panel-https] [-state] [${box.root}/var/lib/o-ui/panel] [-panel-cert] [${box.root}/etc/o-ui/tls/fullchain.pem] [-panel-key] [${box.root}/etc/o-ui/tls/key.pem] [-panel-listen] [0.0.0.0:23456] [-panel-origin] [https://example.com:23456] [-confirm-https] [确认启用HTTPS非本机访问]`), panel);
  assert.match(calls, /runuser LC_ALL=zh_CN\.UTF-8 -u o-ui -- .*-init-panel-admin/);
  // 首次启动前已完成管理员与HTTPS写入
  const order = ['-init-panel-admin', '-enable-panel-https'].map(word => panel.indexOf(word));
  assert.ok(order[0] < order[1]);
  assert.ok(calls.indexOf('systemctl start o-ui') > calls.indexOf('runuser LC_ALL=zh_CN.UTF-8 -u o-ui -- ' + box.root + '/opt/o-ui/bin/o-ui -enable-panel-https'));
  // ACME：Let's Encrypt、独立目录、续期后重载
  const acme = await box.log('acme.log');
  assert.ok(acme.includes(`[--home] [${box.root}/etc/o-ui/acme] [--config-home] [${box.root}/etc/o-ui/acme/data] [--cert-home] [${box.root}/etc/o-ui/acme/certs] [--issue] [--server] [letsencrypt] [-d] [example.com] [--keylength] [ec-256] [--standalone]`), acme);
  assert.ok(acme.includes('[--install-cert] [-d] [example.com] [--ecc]') && acme.includes('[--reloadcmd] [/usr/local/bin/o-ui cert-reload]'), acme);
  assert.doesNotMatch(acme, /shortlived/);
  assert.match(calls, new RegExp(`chown root:o-ui ${box.root}/etc/o-ui/tls/key.pem`));
  assert.equal((await stat(join(box.root, 'etc/o-ui/tls/key.pem'))).mode & 0o777, 0o640);
  assert.equal((await stat(join(box.root, 'etc/o-ui/tls'))).mode & 0o777, 0o750);
  // BBR持久化与原值记录
  assert.equal(await box.read('etc/sysctl.d/99-o-ui-bbr.conf'), '# o-ui: BBR，删除本文件并运行 o-ui bbr off 可回退\nnet.core.default_qdisc=fq\nnet.ipv4.tcp_congestion_control=bbr\n');
  assert.equal(await box.read('proc/sys/net/ipv4/tcp_congestion_control'), 'bbr\n');
  // 防火墙只放行面板端口
  assert.match(calls, /^ufw allow 23456\/tcp comment o-ui panel$/m);
  assert.equal((calls.match(/^ufw /gm) ?? []).length, 1);
  assert.match(calls, /systemctl enable --now o-ui-cert-renew\.timer/);
  assert.deepEqual(await conf(box), { OUI_LANG: 'zh', OUI_PORT: '23456', OUI_MODE: 'acme', OUI_HOST: 'example.com', OUI_CHALLENGE: 'http', OUI_RENEW: 'timer', OUI_VERSION: 'v0.1.0', OUI_FW: 'ufw', OUI_BBR_PREV_QDISC: 'pfifo_fast', OUI_BBR_PREV_CC: 'cubic', OUI_BBR_MODULE: '' });
  assert.equal((await stat(join(box.root, 'etc/o-ui/o-ui.conf'))).mode & 0o777, 0o600);
  assert.ok(!(await box.read('etc/o-ui/o-ui.conf')).includes(password));
  // 终端只显示一次随机密码与HTTPS地址
  assert.equal(out.split(password).length - 1, 1);
  assert.match(out, /登录地址（HTTPS）：https:\/\/example\.com:23456\//);
  assert.match(out, /Xray核心未自动安装/);
  assert.doesNotMatch(out, /http:\/\/(?!127\.0\.0\.1)/);
});

test('英文安装：Cloudflare DNS验证、手动续期、不开BBR，令牌只经环境变量传给acme.sh', async () => {
  const box = await sandbox();
  const answers = ['2', '30001', 'ops_admin', 'correct-horse-battery', 'correct-horse-battery', '1', 'panel.example.org', '2', 'cf-secret-token-123', 'zone123', 'ops@example.org', '2', 'n'];
  const { status, out } = box.run(['install', '--package', box.pkg], answers);
  assert.equal(status, 0, out);
  const acme = await box.log('acme.log'), unit = await box.read('etc/systemd/system/o-ui.service');
  assert.match(acme, /\[--dns\] \[dns_cf\] CF_Token=set CF_Zone_ID=zone123/);
  assert.match(acme, /\[--accountemail\] \[ops@example\.org\]/);
  assert.doesNotMatch(acme + (await box.log('calls.log')) + out, /cf-secret-token-123/);
  assert.ok(unit.includes('Environment=LANG=en_US.UTF-8\n') && unit.includes('Environment=O_UI_DEFAULT_LANG=en\n'));
  assert.equal(existsSync(join(box.root, 'etc/systemd/system/o-ui-cert-renew.timer')), false);
  assert.equal(existsSync(join(box.root, 'etc/sysctl.d/99-o-ui-bbr.conf')), false);
  assert.equal(await box.log('admin-password'), 'correct-horse-battery');
  assert.ok(!out.includes('correct-horse-battery'), '用户输入的密码不回显');
  assert.match(out, /Sign-in URL \(HTTPS\): https:\/\/panel\.example\.org:30001\//);
  assert.match(out, /No active UFW\/firewalld found/);
  assert.doesNotMatch(out.slice(out.indexOf('2) English') + 10), /[一-鿿]/u, '选英文后不再出现中文提示');
  assert.equal((await conf(box)).OUI_RENEW, 'manual');
});

test('IP证书只用HTTP-01并选择短期证书，按3天续期', async () => {
  const box = await sandbox();
  const { status, out } = box.run(['install', '--package', box.pkg], ['1', '24000', 'admin-ip', '', '1', '203.0.113.10', '', '1', 'n']);
  assert.equal(status, 0, out);
  assert.match(await box.log('acme.log'), /\[-d\] \[203\.0\.113\.10\] \[--keylength\] \[ec-256\] \[--standalone\] \[--cert-profile\] \[shortlived\] \[--days\] \[3\]/);
  assert.match(await box.log('oui.log'), /\[-panel-listen\] \[0\.0\.0\.0:24000\] \[-panel-origin\] \[https:\/\/203\.0\.113\.10:24000\]/);
  assert.match(out, /IP证书为Let's Encrypt短期证书/);
});

test('80端口被占用时不使用HTTP-01，可改选仅本机：不开放公网、不放行防火墙', async () => {
  const box = await sandbox();
  await writeFile(join(box.state, 'listening-extra'), '80\n');
  const { status, out } = box.run(['install', '--package', box.pkg], ['1', '25000', 'admin-local', '', '1', 'example.com', '1', '3', 'n'], { FAKE_UFW: '1' });
  assert.equal(status, 0, out);
  assert.match(out, /80端口已被占用/);
  const panel = await box.log('oui.log');
  assert.match(panel, /-init-panel-admin/);
  assert.doesNotMatch(panel, /-enable-panel-https/);
  assert.equal(await box.log('acme.log'), '');
  assert.doesNotMatch(await box.log('calls.log'), /^ufw /m);
  assert.match(out, /ssh -L 25000:127\.0\.0\.1:25000 root@服务器/);
  assert.equal((await conf(box)).OUI_MODE, 'local');
});

test('证书申请失败时保留已初始化管理员并以仅本机启动，停用该ACME条目', async () => {
  const box = await sandbox();
  const { status, out } = box.run(['install', '--package', box.pkg], ['1', '26000', 'admin-fail', '', '1', 'example.com', '1', '', '1', 'n'], { FAKE_ACME_ISSUE_RC: '1' });
  assert.equal(status, 0, out);
  assert.match(out, /HTTPS未配置成功，面板暂以仅本机模式启动/);
  assert.match(await box.log('acme.log'), /\[--remove\] \[-d\] \[example\.com\] \[--ecc\]/);
  assert.doesNotMatch(await box.log('oui.log'), /-enable-panel-https/);
  assert.equal(existsSync(join(box.root, 'etc/systemd/system/o-ui-cert-renew.timer')), false);
  assert.equal((await conf(box)).OUI_MODE, 'local');
  assert.match(await box.log('calls.log'), /systemctl start o-ui/);
});

test('使用已有证书文件：复制到/etc/o-ui/tls并只允许服务组读取', async () => {
  const box = await sandbox();
  await writeFile(join(box.base, 'my.crt'), 'MYCERT\n'); await writeFile(join(box.base, 'my.key'), 'MYKEY\n', { mode: 0o600 });
  const { status, out } = box.run(['install', '--package', box.pkg], ['1', '27000', 'admin-file', '', '2', 'files.example.com', join(box.base, 'my.crt'), join(box.base, 'my.key'), 'n']);
  assert.equal(status, 0, out);
  assert.equal(await box.read('etc/o-ui/tls/fullchain.pem'), 'MYCERT\n');
  assert.equal(await box.read('etc/o-ui/tls/key.pem'), 'MYKEY\n');
  assert.equal((await stat(join(box.root, 'etc/o-ui/tls/key.pem'))).mode & 0o777, 0o640);
  assert.equal(await box.log('acme.log'), '');
  assert.match(await box.log('oui.log'), /\[-panel-origin\] \[https:\/\/files\.example\.com:27000\]/);
  assert.equal((await conf(box)).OUI_MODE, 'file');
});

test('发布包摘要、路径、链接、平台或包内清单不符时拒绝安装且不改系统', async () => {
  const cases: [string, (box: Awaited<ReturnType<typeof sandbox>>) => Promise<string[]>, RegExp][] = [
    ['摘要不符', async box => { await writeFile(join(box.downloads, 'SHA256SUMS'), `${'0'.repeat(64)}  o-ui-linux-amd64.tar.gz\n`); return ['--package', box.pkg]; }, /摘要不符/],
    ['平台不符', async box => ['--package', await makePackage(join(box.base, 'arm'), { arch: 'arm64' })], /发布清单中没有唯一|平台/],
    ['PLATFORM错误', async box => { const dir = join(box.base, 'bad'); await mkdir(dir); return ['--package', await makePackage(dir, { platform: 'linux-arm64' })]; }, /包平台与本机不符/],
    ['包内清单被改', async box => { const dir = join(box.base, 'inner'); await mkdir(dir); return ['--package', await makePackage(dir, { extra: { 'bin/o-ui': 'tampered' }, skipInnerSums: false }).then(async out => { const source = join(dir, 'src-amd64-v0.1.0', 'o-ui'); await writeFile(join(source, 'bin/o-ui'), 'tampered-after-sums'); execFileSync('tar', ['-czf', out, '-C', join(dir, 'src-amd64-v0.1.0'), 'o-ui'], { env: { ...process.env, COPYFILE_DISABLE: '1' } }); await writeFile(join(dir, 'SHA256SUMS'), `${sha(await readFile(out))}  o-ui-linux-amd64.tar.gz\n`); return out; })]; }, /包内清单校验失败/],
    ['路径穿越', async box => { const dir = join(box.base, 'evil'); await mkdir(join(dir, 'x'), { recursive: true }); await writeFile(join(dir, 'x', 'evil'), 'x'); const out = join(dir, 'o-ui-linux-amd64.tar.gz'); execFileSync('tar', ['-czf', out, '-C', join(dir, 'x'), 'evil'], { env: { ...process.env, COPYFILE_DISABLE: '1' } }); await writeFile(join(dir, 'SHA256SUMS'), `${sha(await readFile(out))}  o-ui-linux-amd64.tar.gz\n`); return ['--package', out]; }, /包内路径无效/],
    ['符号链接', async box => { const dir = join(box.base, 'link'); await mkdir(dir); const out = await makePackage(dir); const source = join(dir, 'src-amd64-v0.1.0'); execFileSync('ln', ['-s', '/etc/passwd', join(source, 'o-ui', 'dist', 'leak')]); execFileSync('tar', ['-czf', out, '-C', source, 'o-ui'], { env: { ...process.env, COPYFILE_DISABLE: '1' } }); await writeFile(join(dir, 'SHA256SUMS'), `${sha(await readFile(out))}  o-ui-linux-amd64.tar.gz\n`); return ['--package', out]; }, /链接或特殊文件/],
  ];
  for (const [name, prepare, message] of cases) {
    const box = await sandbox();
    const args = await prepare(box);
    const { status, out } = box.run(['install', ...args], ['1', '23456', 'admin-x', '', '3', 'n']);
    assert.notEqual(status, 0, name);
    assert.match(out, message, `${name}: ${out}`);
    assert.equal(existsSync(join(box.root, 'opt/o-ui')), false, name);
    assert.equal(existsSync(join(box.root, 'usr/local/bin/o-ui')), false, name);
    assert.equal(await box.log('calls.log'), '', name);
  }
});

test('从GitHub Release下载最新或指定版本，并按发布清单校验', async () => {
  const box = await sandbox();
  const { status, out } = box.run(['install'], ['1', '23456', 'admin-dl', '', '3', 'n'], { O_UI_REPO: 'owner/o-ui' });
  assert.equal(status, 0, out);
  const calls = await box.log('calls.log');
  assert.match(calls, /^curl https:\/\/github\.com\/owner\/o-ui\/releases\/latest\/download\/o-ui-linux-amd64\.tar\.gz$/m);
  assert.match(calls, /^curl https:\/\/github\.com\/owner\/o-ui\/releases\/latest\/download\/SHA256SUMS$/m);
  const second = await sandbox();
  const pinned = second.run(['install', '--version', 'v0.1.0'], ['1', '23456', 'admin-dl', '', '3', 'n'], { O_UI_REPO: 'owner/o-ui', FAKE_ARCH: 'aarch64' });
  assert.notEqual(pinned.status, 0);
  assert.match(await second.log('calls.log'), /releases\/download\/v0\.1\.0\/o-ui-linux-arm64\.tar\.gz/);
  const official = await sandbox();
  assert.equal(official.run(['install'], ['1', '23456', 'admin-dl', '', '3', 'n']).status, 0);
  assert.match(await official.log('calls.log'), /^curl https:\/\/github\.com\/tpxcer\/o-ui\/releases\/latest\/download\/o-ui-linux-amd64\.tar\.gz$/m);
  const invalid = (await sandbox()).run(['install'], ['1'], { O_UI_REPO: 'not a repo' });
  assert.notEqual(invalid.status, 0); assert.match(invalid.out, /发布仓库无效/);
});

test('acme.sh只按固定版本摘要安装到独立目录，摘要不符时拒绝', async () => {
  const box = await sandbox({ acme: false });
  const dir = join(box.base, 'acme-src', 'acme.sh-3.1.6'); await mkdir(dir, { recursive: true }); await writeFile(join(dir, 'acme.sh'), fakeAcme, { mode: 0o755 });
  execFileSync('tar', ['-czf', join(box.downloads, '3.1.6.tar.gz'), '-C', join(box.base, 'acme-src'), 'acme.sh-3.1.6'], { env: { ...process.env, COPYFILE_DISABLE: '1' } });
  await mkdir(join(box.base, 'tools'), { recursive: true });
  const { status, out } = box.run(['install', '--package', box.pkg], ['1', '23456', 'admin-acme', '', '1', 'example.com', '1', '', '1', 'n']);
  assert.notEqual(status, 0);
  assert.match(out, /acme\.sh摘要不符/);
  assert.match(await box.log('calls.log'), /^curl https:\/\/github\.com\/acmesh-official\/acme\.sh\/archive\/refs\/tags\/3\.1\.6\.tar\.gz$/m);
  assert.equal(existsSync(join(box.root, 'etc/o-ui/acme/acme.sh')), false);
  assert.equal(existsSync(join(box.state, 'active')), false);
});

test('升级保留数据并保留上一版本；新版本启动失败自动回退', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], ['1', '23456', 'admin-up', '', '3', 'n']).status, 0);
  await writeFile(join(box.root, 'var/lib/o-ui/panel/marker'), 'keep');
  const v2 = join(box.base, 'v2'); await mkdir(v2);
  const ok = box.run(['update', '--package', await makePackage(v2, { version: 'v0.2.0' })]);
  assert.equal(ok.status, 0, ok.out);
  assert.equal(await box.read('opt/o-ui/VERSION'), 'v0.2.0\n');
  assert.equal(await box.read('opt/o-ui.prev/VERSION'), 'v0.1.0\n');
  assert.equal(await box.read('var/lib/o-ui/panel/marker'), 'keep');
  assert.equal((await conf(box)).OUI_VERSION, 'v0.2.0');
  const calls = await box.log('calls.log');
  assert.ok(calls.lastIndexOf('systemctl stop o-ui') < calls.lastIndexOf('systemctl start o-ui'));
  const v3 = join(box.base, 'v3'); await mkdir(v3);
  const bad = box.run(['update', '--package', await makePackage(v3, { version: 'v0.3.0' })], [], { FAKE_FAIL_VERSION: 'v0.3.0' });
  assert.notEqual(bad.status, 0);
  assert.match(bad.out, /已回退到 v0\.2\.0/);
  assert.equal(await box.read('opt/o-ui/VERSION'), 'v0.2.0\n');
  assert.equal((await conf(box)).OUI_VERSION, 'v0.2.0');
  assert.ok(existsSync(join(box.state, 'active')));
});

test('卸载默认保留数据与证书；--purge须输入DELETE并删除全部', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], ['1', '23456', 'admin-rm', '', '1', 'example.com', '1', '', '1', 'y', 'y'], { FAKE_UFW: '1' }).status, 0);
  const kept = box.run(['uninstall'], ['y', 'y', 'n'], { FAKE_UFW: '1' });
  assert.equal(kept.status, 0, kept.out);
  for (const gone of ['opt/o-ui', 'usr/local/bin/o-ui', 'etc/systemd/system/o-ui.service', 'etc/systemd/system/o-ui-cert-renew.timer']) assert.equal(existsSync(join(box.root, gone)), false, gone);
  for (const keep of ['var/lib/o-ui/panel/panel.sqlite', 'etc/o-ui/tls/key.pem', 'etc/o-ui/o-ui.conf', 'etc/sysctl.d/99-o-ui-bbr.conf']) assert.ok(existsSync(join(box.root, keep)), keep);
  const calls = await box.log('calls.log');
  assert.match(calls, /^ufw delete allow 23456\/tcp$/m);
  assert.match(calls, /systemctl disable --now o-ui/);
  assert.doesNotMatch(calls, /userdel/);
  // 重新安装沿用数据与管理员
  const again = box.run(['install', '--package', box.pkg], ['1', '23456', '3', 'n'], { USE_SOURCE: '1' });
  assert.equal(again.status, 0, again.out);
  assert.match(again.out, /检测到已有数据目录/);
  assert.equal(((await box.log('oui.log')).match(/-init-panel-admin/g) ?? []).length, 1);
  assert.match(await box.log('oui.log'), /-recover-panel-access/);
  const refused = box.run(['uninstall', '--purge'], ['yes']);
  assert.notEqual(refused.status, 0);
  assert.ok(existsSync(join(box.root, 'var/lib/o-ui')));
  const purged = box.run(['uninstall', '--purge'], ['DELETE', 'y']);
  assert.equal(purged.status, 0, purged.out);
  for (const gone of ['var/lib/o-ui', 'etc/o-ui', 'opt/o-ui', 'usr/local/bin/o-ui', 'etc/sysctl.d/99-o-ui-bbr.conf']) assert.equal(existsSync(join(box.root, gone)), false, gone);
  assert.match(await box.log('calls.log'), /userdel o-ui/);
});

test('BBR：不支持的内核跳过，应用失败撤销，off按记录原值回退', async () => {
  const old = await sandbox();
  const skipped = old.run(['install', '--package', old.pkg], ['1', '23456', 'admin-k', '', '3'], { FAKE_KERNEL: '4.4.0-210-generic' });
  assert.equal(skipped.status, 0, skipped.out);
  assert.match(skipped.out, /当前内核不支持BBR/);
  const container = await sandbox();
  assert.equal(container.run(['install', '--package', container.pkg], ['1', '23456', 'admin-c', '', '3', 'y'], { FAKE_SYSCTL_FAIL: '1' }).status, 0);
  assert.equal(existsSync(join(container.root, 'etc/sysctl.d/99-o-ui-bbr.conf')), false);
  assert.equal((await conf(container)).OUI_BBR_PREV_CC, '');
  const box = await sandbox();
  await writeFile(join(box.root, 'proc/sys/net/ipv4/tcp_available_congestion_control'), 'reno cubic\n');
  assert.equal(box.run(['install', '--package', box.pkg], ['1', '23456', 'admin-b', '', '3', 'y'], { FAKE_BBR_MODULE: '1' }).status, 0);
  assert.equal(await box.read('etc/modules-load.d/o-ui-bbr.conf'), 'tcp_bbr\n');
  assert.match(box.run(['bbr', 'status']).out, /fq\/bbr/);
  const off = box.run(['bbr', 'off']);
  assert.equal(off.status, 0, off.out);
  assert.equal(existsSync(join(box.root, 'etc/sysctl.d/99-o-ui-bbr.conf')), false);
  assert.equal(existsSync(join(box.root, 'etc/modules-load.d/o-ui-bbr.conf')), false);
  assert.equal(await box.read('proc/sys/net/ipv4/tcp_congestion_control'), 'cubic\n');
  assert.equal(await box.read('proc/sys/net/core/default_qdisc'), 'pfifo_fast\n');
  assert.equal((await conf(box)).OUI_BBR_PREV_CC, '');
});

test('续期回调修正证书权限，仅在面板运行时重启；手动续期区分未到期', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], ['1', '23456', 'admin-r', '', '1', 'example.com', '1', '', '2', 'n']).status, 0);
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
  assert.match(await box.log('acme.log'), /\[--renew\] \[-d\] \[example\.com\] \[--ecc\]/);
});

test('o-ui cert可在安装后改为HTTPS：先停面板再离线写入，并启用续期', async () => {
  const box = await sandbox();
  assert.equal(box.run(['install', '--package', box.pkg], ['1', '23456', 'admin-c', '', '3', 'n'], { FAKE_UFW: '1' }).status, 0);
  const { status, out } = box.run(['cert'], ['1', 'later.example.com', '2', 'token-x', '', '', '1', 'y'], { FAKE_UFW: '1' });
  assert.equal(status, 0, out);
  const calls = await box.log('calls.log');
  const https = calls.indexOf('-enable-panel-https');
  assert.ok(calls.lastIndexOf('systemctl stop o-ui', https) > 0 && calls.indexOf('systemctl start o-ui', https) > https);
  assert.match(calls, /^ufw allow 23456\/tcp comment o-ui panel$/m);
  assert.match(out, /https:\/\/later\.example\.com:23456\//);
  assert.equal((await conf(box)).OUI_MODE, 'acme');
});

test('非root、非Linux、不支持的架构、无systemd或已安装时拒绝', async () => {
  const box = await sandbox();
  assert.match(box.run(['install', '--package', box.pkg], [], { FAKE_UID: '1000' }).out, /请用root运行|run as root/);
  assert.match(box.run(['install', '--package', box.pkg], [], { FAKE_OS: 'Darwin' }).out, /仅支持Linux|only Linux/);
  assert.match(box.run(['install', '--package', box.pkg], [], { FAKE_ARCH: 'armv7l' }).out, /x86_64/);
  await rm(join(box.root, 'run/systemd'), { recursive: true });
  assert.match(box.run(['install', '--package', box.pkg]).out, /systemd/);
  await mkdir(join(box.root, 'run/systemd/system'), { recursive: true });
  assert.equal(box.run(['install', '--package', box.pkg], ['1', '23456', 'admin-o', '', '3', 'n']).status, 0);
  const again = box.run(['install', '--package', box.pkg], ['1']);
  assert.notEqual(again.status, 0); assert.match(again.out, /已安装/);
  assert.equal(await box.log('calls.log').then(text => text.includes('rm -rf')), false);
});
