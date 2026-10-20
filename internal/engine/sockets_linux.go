package engine

import (
	"context"
	"independent-panel/internal/i18n"
	"os"
	"path/filepath"
	"strconv"

	gnet "github.com/shirou/gopsutil/v4/net"
)

func readSockets(ctx context.Context, pid int32) (int, int, error) {
	// gopsutil空inode分支可能吞掉权限错误，先验证本次PID的fd可读。
	root := filepath.Join("/proc", strconv.Itoa(int(pid)), "fd")
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, 0, err
	}
	for _, entry := range entries {
		if _, err := os.Readlink(filepath.Join(root, entry.Name())); err != nil && !os.IsNotExist(err) {
			return 0, 0, err
		}
	}
	connections, err := gnet.ConnectionsPidWithoutUidsWithContext(ctx, "inet", pid)
	if err != nil {
		return 0, 0, err
	}
	// 自有运行核心必有活动入站；无任何IP记录不能证明成功读取了空集合。
	if len(connections) == 0 {
		return 0, 0, i18n.New("无法确认核心网络套接字", "Cannot confirm the core's network sockets")
	}
	tcp, udp := socketCounts(connections, pid)
	return tcp, udp, ctx.Err()
}
