#!/bin/sh
# 仅在新建无网卡内存客体运行，输入为只读测试ISO，所有状态仅属于临时非特权用户。
set -eu
if [ "$(uname -sm)" != "Linux aarch64" ] || [ "$(id -u)" = 0 ] || [ "$#" != 1 ]; then
  echo '仅支持专用Linux ARM64客体的非特权用户，须指定只读输入目录' >&2
  exit 1
fi
qa_input=$1
case "$qa_input" in /*) ;; *) exit 1 ;; esac
cd "$qa_input"
sha256sum -c SHA256SUMS || exit 1
qa_root=$(mktemp -d /tmp/ixp-arm64-run.XXXXXX)
mkdir -p "$qa_root/work/internal/localapi" "$qa_root/logs"
tar -xf "$qa_input/dist.tar" -C "$qa_root/work"
export PANEL_PROCESS_QA_BINARY="$qa_input/panel"
qa_case() {
  qa_name=$1
  shift
  echo "PANEL_QA_CASE_BEGIN $qa_name"
  qa_exit=0
  "$@" > "$qa_root/logs/$qa_name.log" 2>&1 || qa_exit=$?
  awk -v name="$qa_name" -v code="$qa_exit" '
    /^[[:space:]]*--- PASS:/ {p++}
    /^[[:space:]]*--- SKIP:/ {s++}
    /^[[:space:]]*--- FAIL:/ {f++}
    END {printf "PANEL_QA_CASE_END %s exit=%d pass=%d skip=%d fail=%d\n",name,code,p,s,f}
  ' "$qa_root/logs/$qa_name.log"
  if [ "$qa_exit" != 0 ]; then tail -100 "$qa_root/logs/$qa_name.log"; return "$qa_exit"; fi
}
echo "PANEL_QA_ENV $(uname -a) uid=$(id -u)"
ip addr show
cd "$qa_root/work/internal/localapi"
for qa_pkg in backup config core diagnostics engine localapi monitor store traffic; do
  qa_case "baseline-$qa_pkg" "$qa_input/$qa_pkg.test" -test.v -test.count=1 -test.timeout=4m
done
export PANEL_STAGED_QA_INPUT="$qa_input"
export PANEL_STAGED_QA_DIR="$qa_root/core"
qa_case official-install "$qa_input/core.test" -test.v -test.run '^TestOfficialStagedInstallation$' -test.count=1 -test.timeout=2m
unset PANEL_STAGED_QA_DIR PANEL_STAGED_QA_INPUT
export PANEL_PROCESS_QA=1
qa_case process-boundaries "$qa_input/localapi.test" -test.v -test.run '^(TestPanelAccessRecoveryProcess|TestPanelStateOwnership|TestPanelHTTPSProcessLifecycle|TestPanelListenerConflict)$' -test.count=1 -test.timeout=3m
export PANEL_RUNTIME_QA_CORE="$qa_root/core"
qa_case official-runtime "$qa_input/localapi.test" -test.v -test.run '^(TestOfficialRuntimeAPI|TestOfficialFourExportedConnections|TestOfficialPublicHTTPSSubscription|TestOfficialPanelAccessRecovery|TestOfficialHistoryProcessRecovery|TestOfficialProxyEndpointBoundaries)$' -test.count=1 -test.timeout=8m
qa_case official-settings "$qa_input/localapi.test" -test.v -test.run '^(TestOfficialDNSSettings|TestOfficialRoutingRules|TestOfficialCommonOutbounds|TestOfficialBalancerRoutes|TestOfficialDoHCertificateRejection)$' -test.count=1 -test.timeout=8m
qa_case official-tcp-limits "$qa_input/localapi.test" -test.v -test.run '^TestOfficialTrafficLimits$' -test.count=1 -test.timeout=8m
qa_case official-udp-limits "$qa_input/localapi.test" -test.v -test.run '^TestOfficialUDPTrafficLimits$' -test.count=1 -test.timeout=8m
echo PANEL_QA_LOGS_BEGIN
for qa_log in "$qa_root"/logs/*.log; do
  echo "PANEL_QA_LOG $qa_log"
  sed -n '/^=== RUN/!{/^=== NAME/!{/^=== PAUSE/!{/^=== CONT/!p;};};};' "$qa_log"
done
echo PANEL_QA_LOGS_END
echo 'PANEL_QA_ALL_PASSED'
echo "PANEL_QA_RESULT_ROOT $qa_root (仅客体内存，关机后消失)"
