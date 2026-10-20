package localapi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

var errUDPNoResponse = errors.New("UDP目标回包超时")

type socksUDPSession struct {
	control net.Conn
	socket  *net.UDPConn
	relay   *net.UDPAddr
}

func validateUDPTestTarget(target *net.UDPAddr) error {
	if target == nil || target.IP.To4() == nil || !target.IP.IsLoopback() || target.Port < 1 || target.Port > 65535 {
		return errors.New("UDP测试目标必须是回环IPv4")
	}
	return nil
}

func openSOCKSUDP(proxy string) (*socksUDPSession, error) {
	proxyAddress, err := netip.ParseAddrPort(proxy)
	if err != nil || !proxyAddress.Addr().Is4() || !proxyAddress.Addr().IsLoopback() || proxyAddress.Port() == 0 {
		return nil, errors.New("SOCKS测试代理必须是回环IPv4")
	}
	control, err := net.DialTimeout("tcp4", proxy, 3*time.Second)
	if err != nil {
		return nil, err
	}
	session := &socksUDPSession{control: control}
	ready := false
	defer func() {
		if !ready {
			session.Close()
		}
	}()
	_ = control.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = control.Write([]byte{5, 1, 0}); err != nil {
		return nil, err
	}
	var greeting [2]byte
	if _, err = io.ReadFull(control, greeting[:]); err != nil {
		return nil, err
	}
	if greeting != [2]byte{5, 0} {
		return nil, errors.New("SOCKS测试认证协商失败")
	}
	socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, err
	}
	session.socket = socket
	address := socket.LocalAddr().(*net.UDPAddr)
	association := []byte{5, 3, 0, 1, 127, 0, 0, 1, 0, 0}
	binary.BigEndian.PutUint16(association[8:], uint16(address.Port))
	if _, err = control.Write(association); err != nil {
		return nil, err
	}
	var reply [10]byte
	if _, err = io.ReadFull(control, reply[:]); err != nil {
		return nil, err
	}
	if !bytes.Equal(reply[:4], []byte{5, 0, 0, 1}) {
		return nil, errors.New("SOCKS测试UDP关联无效")
	}
	relay := &net.UDPAddr{IP: net.IP(reply[4:8]), Port: int(binary.BigEndian.Uint16(reply[8:]))}
	if relay.IP.IsUnspecified() {
		relay.IP = control.RemoteAddr().(*net.TCPAddr).IP
	}
	if !relay.IP.IsLoopback() || relay.Port < 1 {
		return nil, errors.New("SOCKS测试中继不是回环地址")
	}
	session.relay = relay
	ready = true
	return session, nil
}

func (s *socksUDPSession) Close() {
	if s.socket != nil {
		_ = s.socket.Close()
	}
	_ = s.control.Close()
}

func (s *socksUDPSession) Exchange(target *net.UDPAddr, payload []byte) error {
	if err := validateUDPTestTarget(target); err != nil {
		return err
	}
	packet := append([]byte{0, 0, 0, 1}, target.IP.To4()...)
	packet = binary.BigEndian.AppendUint16(packet, uint16(target.Port))
	packet = append(packet, payload...)
	_ = s.socket.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := s.socket.WriteToUDP(packet, s.relay); err != nil {
		return err
	}
	buffer := make([]byte, 65535)
	n, sender, err := s.socket.ReadFromUDP(buffer)
	if err != nil {
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return errUDPNoResponse
		}
		return err
	}
	if !sender.IP.Equal(s.relay.IP) || sender.Port != s.relay.Port || !bytes.Equal(buffer[:n], packet) {
		return errors.New("SOCKS测试UDP来源、目标、分片或载荷不匹配")
	}
	return nil
}

// 只接受回环IPv4中继和目标；认证拒绝必须发生在成功建立UDP关联之后。
func socksUDPExchange(proxy string, target *net.UDPAddr, payload []byte) error {
	if err := validateUDPTestTarget(target); err != nil {
		return err
	}
	session, err := openSOCKSUDP(proxy)
	if err != nil {
		return err
	}
	defer session.Close()
	return session.Exchange(target, payload)
}

func runtimeUDPEcho(t *testing.T) (*net.UDPAddr, *atomic.Uint64) {
	t.Helper()
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	hits := &atomic.Uint64{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 65535)
		for {
			n, source, err := listener.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			hits.Add(1)
			_, _ = listener.WriteToUDP(buffer[:n], source)
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return listener.LocalAddr().(*net.UDPAddr), hits
}

func TestSOCKSUDPExchange(t *testing.T) {
	for _, mode := range []string{"echo", "persistent", "corrupt-payload", "corrupt-target", "fragmented", "reject-association"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			relay, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer relay.Close()
			done := make(chan error, 1)
			go func() {
				connection, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer connection.Close()
				_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
				var request [3]byte
				_, err = io.ReadFull(connection, request[:])
				if err == nil && request != [3]byte{5, 1, 0} {
					err = errors.New("bad test greeting")
				}
				if err == nil {
					_, err = connection.Write([]byte{5, 0})
				}
				var association [10]byte
				if err == nil {
					_, err = io.ReadFull(connection, association[:])
				}
				reply := []byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}
				binary.BigEndian.PutUint16(reply[8:], uint16(relay.LocalAddr().(*net.UDPAddr).Port))
				if mode == "reject-association" {
					reply[1] = 1
				}
				if err == nil {
					_, err = connection.Write(reply)
				}
				packets := 1
				if mode == "persistent" {
					packets = 2
				}
				for i := 0; i < packets && err == nil && mode != "reject-association"; i++ {
					_ = relay.SetDeadline(time.Now().Add(5 * time.Second))
					buffer := make([]byte, 256)
					var n int
					var sender *net.UDPAddr
					n, sender, err = relay.ReadFromUDP(buffer)
					if err == nil && n > 10 {
						if mode == "corrupt-payload" {
							buffer[n-1] ^= 1
						}
						if mode == "corrupt-target" {
							buffer[9] ^= 1
						}
						if mode == "fragmented" {
							buffer[2] = 1
						}
						_, err = relay.WriteToUDP(buffer[:n], sender)
					}
				}
				done <- err
			}()
			target := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 30001}
			if mode == "persistent" {
				var session *socksUDPSession
				session, err = openSOCKSUDP(listener.Addr().String())
				if err == nil {
					err = session.Exchange(target, []byte("first-packet"))
					if err == nil {
						err = session.Exchange(target, []byte("second-packet"))
					}
					session.Close()
				}
			} else {
				err = socksUDPExchange(listener.Addr().String(), target, []byte("independent-udp-test"))
			}
			if (err == nil) != (mode == "echo" || mode == "persistent") || errors.Is(err, errUDPNoResponse) {
				t.Fatal("UDP test helper accepted corrupt reply or treated setup failure as remote rejection", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSOCKSUDPExchangeRejectsUnsafeAddresses(t *testing.T) {
	loopback := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 30001}
	for _, tc := range []struct {
		name, proxy string
		target      *net.UDPAddr
	}{
		{"nil-target", "127.0.0.1:1", nil},
		{"public-target", "127.0.0.1:1", &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 30001}},
		{"invalid-target-port", "127.0.0.1:1", &net.UDPAddr{IP: loopback.IP, Port: 65536}},
		{"public-proxy", "192.0.2.1:1", loopback},
		{"hostname-proxy", "localhost:1", loopback},
		{"ipv6-proxy", "[::1]:1", loopback},
		{"zero-proxy-port", "127.0.0.1:0", loopback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := socksUDPExchange(tc.proxy, tc.target, []byte("unsafe-address")); err == nil || errors.Is(err, errUDPNoResponse) {
				t.Fatal("unsafe address accepted or mistaken for remote rejection", err)
			}
		})
	}
}
