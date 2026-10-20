package engine

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"independent-panel/internal/diagnostics"
)

func hasDiagnostic(log *diagnostics.Log, code string) bool {
	for _, e := range log.Snapshot().Entries {
		if e.Code == code {
			return true
		}
	}
	return false
}

func TestManagedDiagnosticsLifecycle(t *testing.T) {
	l := diagnostics.New()
	c := fakeCandidate(t, freeAddress(t), true, false)
	c.Diagnostics = l
	const sentinel = "fixture-only-private-identity-cookie-<script>"
	data, _ := json.Marshal(map[string]any{"Invalid": true, "Diagnostic": "Failed to load config " + sentinel})
	if err := os.WriteFile(c.File, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Preflight(context.Background(), c); err == nil || !hasDiagnostic(l, "core.config") || !hasDiagnostic(l, "xray.preflight.failed") {
		t.Fatal("预检诊断未到达父进程")
	}
	encoded, _ := json.Marshal(l.Snapshot())
	if strings.Contains(string(encoded), sentinel) {
		t.Fatal("核心原文泄漏")
	}

	e := &Engine{}
	defer e.Close()
	good := fakeCandidate(t, freeAddress(t), false, false)
	good.Diagnostics = l
	if err := e.Apply(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if !hasDiagnostic(l, "xray.stopped") || hasDiagnostic(l, "xray.unexpected") {
		t.Fatal("正常停止被当作异常")
	}
	if err := e.Apply(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	foreign, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	bad := fakeCandidate(t, foreign.Addr().String(), false, false)
	bad.Diagnostics = l
	if e.Apply(context.Background(), bad) == nil || !e.Status().Running || !hasDiagnostic(l, "core.port_busy") || !hasDiagnostic(l, "xray.rollback.ok") {
		t.Fatal("启动占用及恢复诊断缺失")
	}
	if err := e.process.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.process.done:
	case <-time.After(5 * time.Second):
		t.Fatal("异常监督未退出")
	}
	if !hasDiagnostic(l, "xray.unexpected") {
		t.Fatal("异常退出未记录")
	}
}
