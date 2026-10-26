package localapi

import (
	"bufio"
	"crypto/tls"
	"independent-panel/internal/i18n"
	"net"
	"net/http"
	"sync"
	"time"
)

// ListenAndServePanelTLS在同一端口接收TLS及只用于跳转的明文HTTP。
func ListenAndServePanelTLS(server *http.Server, access *PanelAccess) error {
	if server.TLSConfig == nil {
		return i18n.New("未配置面板TLS，拒绝启动HTTPS监听", "Panel TLS configuration is missing; refusing to start the HTTPS listener")
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}
	// Serve会安装HTTP/2处理器；连接使用独立快照，避免并发修改TLS配置。
	server.TLSConfig = server.TLSConfig.Clone()
	server.TLSConfig.NextProtos = []string{"h2", "http/1.1"}
	listener = newPanelProtocolListener(listener, server.TLSConfig.Clone())
	defer listener.Close()
	server.Handler = panelHTTPSRedirect(server.Handler, access)
	return server.Serve(listener)
}

func panelHTTPSRedirect(next http.Handler, access *PanelAccess) http.Handler {
	allowed := map[string]bool{}
	for _, host := range access.hosts() {
		allowed[host] = true
	}
	origin := access.Data.Origin
	if !access.Data.Public {
		origin = "https://" + access.Data.Listen
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Connection", "close")
		w.Header().Set("Cache-Control", "no-store")
		remote, _, err := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(remote)
		if err != nil || ip == nil || (!access.Data.Public && !ip.IsLoopback()) || !allowed[r.Host] {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.IsAbs() || r.URL.Host != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// 不重放明文登录、配置或上传请求，也不进入任何业务处理器。
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Upgrade", "TLS/1.2")
			w.WriteHeader(http.StatusUpgradeRequired)
			return
		}
		http.Redirect(w, r, origin+r.URL.RequestURI(), http.StatusPermanentRedirect)
	})
}

type panelBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *panelBufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

type panelProtocolListener struct {
	net.Listener
	ready   chan panelClassifiedConn
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	pending map[net.Conn]struct{}
	err     error
}

type panelClassifiedConn struct{ conn, raw net.Conn }

func newPanelProtocolListener(listener net.Listener, config *tls.Config) *panelProtocolListener {
	l := &panelProtocolListener{Listener: listener, ready: make(chan panelClassifiedConn), done: make(chan struct{}), pending: map[net.Conn]struct{}{}}
	go func() {
		var delay time.Duration
		for {
			conn, err := listener.Accept()
			if err != nil {
				if temporary, ok := err.(net.Error); ok && temporary.Temporary() {
					if delay == 0 {
						delay = 5 * time.Millisecond
					} else {
						delay = min(2*delay, time.Second)
					}
					select {
					case <-time.After(delay):
						continue
					case <-l.done:
						return
					}
				}
				l.mu.Lock()
				l.err = err
				l.mu.Unlock()
				_ = l.Close()
				return
			}
			delay = 0
			l.mu.Lock()
			select {
			case <-l.done:
				_ = conn.Close()
				l.mu.Unlock()
				return
			default:
			}
			// 慢连接不能阻塞后续TLS连接，也不能无限创建探测协程。
			if len(l.pending) >= 128 {
				_ = conn.Close()
				l.mu.Unlock()
				continue
			}
			l.pending[conn] = struct{}{}
			l.mu.Unlock()
			go l.classify(conn, config)
		}
	}()
	return l
}

func (l *panelProtocolListener) classify(conn net.Conn, config *tls.Config) {
	defer func() { l.mu.Lock(); delete(l.pending, conn); l.mu.Unlock() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(conn)
	first, err := reader.Peek(1)
	if err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	var selected net.Conn = &panelBufferedConn{Conn: conn, reader: reader}
	if first[0] == 0x16 { // TLS握手记录；保留原始首字节交给标准库校验。
		selected = tls.Server(selected, config)
	}
	select {
	case l.ready <- panelClassifiedConn{conn: selected, raw: conn}:
	case <-l.done:
		_ = conn.Close()
	}
}

func (l *panelProtocolListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.ready:
		l.mu.Lock()
		delete(l.pending, conn.raw)
		l.mu.Unlock()
		return conn.conn, nil
	case <-l.done:
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.err != nil {
			return nil, l.err
		}
		return nil, net.ErrClosed
	}
}

func (l *panelProtocolListener) Close() error {
	var err error
	l.once.Do(func() {
		close(l.done)
		err = l.Listener.Close()
		l.mu.Lock()
		defer l.mu.Unlock()
		for conn := range l.pending {
			_ = conn.Close()
		}
	})
	return err
}
