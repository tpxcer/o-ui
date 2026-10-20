package engine

import (
	"bytes"
	"context"
	"independent-panel/internal/i18n"
	"os/exec"
	"strconv"
	"strings"
)

type socketOutput struct{ bytes.Buffer }

func (b *socketOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 4<<20 {
		return 0, i18n.New("套接字响应过大", "Socket response too large")
	}
	return b.Buffer.Write(data)
}

func readSockets(ctx context.Context, pid int32) (int, int, error) {
	// 固定系统程序、数字字段，无DNS/地址输出；非零退出不能当空集合。
	cmd := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-a", "-p", strconv.Itoa(int(pid)), "-i", "-FpftPT")
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin"}
	var out, diagnostic socketOutput
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	err := cmd.Run()
	if ctx.Err() != nil {
		return 0, 0, ctx.Err()
	}
	if err != nil || diagnostic.Len() != 0 {
		return 0, 0, i18n.New("无法完整读取自有核心套接字", "Could not fully read the core's sockets")
	}
	return parseSockets(out.String(), pid)
}

func parseSockets(output string, pid int32) (int, int, error) {
	tcp, udp := 0, 0
	owner, family, protocol, status := "", "", "", ""
	file := false
	flush := func() bool {
		if !file {
			return true
		}
		if owner != strconv.Itoa(int(pid)) || (family != "IPv4" && family != "IPv6") || (protocol != "TCP" && protocol != "UDP") {
			return false
		}
		if protocol == "TCP" && status == "" {
			return false
		}
		if protocol == "TCP" && status == "ESTABLISHED" {
			tcp++
		}
		if protocol == "UDP" {
			udp++
		}
		return true
	}
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			if !flush() {
				return 0, 0, i18n.New("套接字结构无效", "Invalid socket structure")
			}
			owner, file = line[1:], false
		case 'f':
			if !flush() {
				return 0, 0, i18n.New("套接字结构无效", "Invalid socket structure")
			}
			file, family, protocol, status = true, "", "", ""
		case 't':
			family = line[1:]
		case 'P':
			protocol = line[1:]
		case 'T':
			if strings.HasPrefix(line, "TST=") {
				status = strings.TrimPrefix(line, "TST=")
			}
		default:
			return 0, 0, i18n.New("未知套接字字段", "Unknown socket field")
		}
	}
	if !file || !flush() {
		return 0, 0, i18n.New("套接字响应不完整", "Incomplete socket response")
	}
	return tcp, udp, nil
}
