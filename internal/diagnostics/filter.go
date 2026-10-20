package diagnostics

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// Filter丢弃超长行，只发送固定代码；绝不把原行当日志消息。
type Filter struct {
	mu       sync.Mutex
	line     []byte
	overflow bool
	emit     func(string)
	last     map[string]time.Time
}

func NewFilter(emit func(string)) *Filter { return &Filter{emit: emit, last: map[string]time.Time{}} }

func (f *Filter) Write(data []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range data {
		if b == '\n' {
			f.flush()
		} else if !f.overflow {
			if len(f.line) == 4096 {
				f.line = nil
				f.overflow = true
			} else {
				f.line = append(f.line, b)
			}
		}
	}
	return len(data), nil
}
func (f *Filter) Flush() { f.mu.Lock(); defer f.mu.Unlock(); f.flush() }
func (f *Filter) flush() {
	code := classify(string(f.line))
	if f.overflow {
		code = "core.long_line"
	}
	f.line, f.overflow = nil, false
	if code == "" {
		return
	}
	now := time.Now()
	if now.Sub(f.last[code]) < time.Second {
		return
	}
	f.last[code] = now
	f.emit(code)
}

func classify(line string) string {
	text := strings.ToLower(line)
	for _, rule := range []struct {
		code    string
		phrases []string
	}{
		{"core.port_busy", []string{"address already in use"}},
		{"core.permission", []string{"permission denied", "operation not permitted"}},
		{"core.file_missing", []string{"no such file or directory"}},
		{"core.certificate", []string{"x509:", "tls:", "failed to load certificate", "failed to parse certificate", "failed to parse private key"}},
		{"core.config", []string{"failed to read config", "failed to load config", "failed to parse", "invalid uuid", "unknown protocol", "invalid character"}},
		{"core.dns", []string{"no such host", "failed to lookup", "failed to resolve"}},
		{"core.network", []string{"connection refused", "network is unreachable", "i/o timeout", "context deadline exceeded"}},
	} {
		for _, phrase := range rule.phrases {
			if strings.Contains(text, phrase) {
				return rule.code
			}
		}
	}
	if strings.Contains(text, "[warning]") || strings.Contains(text, "[error]") || strings.HasPrefix(text, "failed to start") || strings.HasPrefix(text, "panic:") {
		return "core.hidden"
	}
	return ""
}

// Receiver再次白名单验证监督管道，异常/非JSON输出不进入日志。
type Receiver struct {
	mu       sync.Mutex
	line     []byte
	overflow bool
	log      *Log
}

func NewReceiver(log *Log) *Receiver {
	return &Receiver{log: log}
}

func (r *Receiver) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range data {
		if b == '\n' {
			if !r.overflow {
				DecodeCode(r.line, r.log)
			}
			r.line, r.overflow = nil, false
		} else if !r.overflow {
			if len(r.line) == 128 {
				r.line = nil
				r.overflow = true
			} else {
				r.line = append(r.line, b)
			}
		}
	}
	return len(data), nil
}

func DecodeCode(line []byte, log *Log) {
	var code string
	if len(line) > 128 || json.Unmarshal(line, &code) != nil || !strings.HasPrefix(code, "core.") {
		return
	}
	log.Add(code)
}
