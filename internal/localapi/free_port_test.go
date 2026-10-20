package localapi

import (
	"net"
	"testing"
)

func TestFreePortSkipsSavedAndBusyPorts(t *testing.T) {
	busy, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	taken := busy.Addr().(*net.TCPAddr).Port
	free, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	candidate := free.Addr().(*net.TCPAddr).Port
	free.Close()
	saved := candidate + 1
	sequence := []int{saved, taken, candidate}
	port, ok := freePort(map[int]bool{saved: true}, false, func() int { next := sequence[0]; sequence = sequence[1:]; return next })
	if !ok || port != candidate {
		t.Fatal("must skip saved and busy ports", port, ok)
	}
	if _, ok = freePort(map[int]bool{}, true, func() int { return taken }); !ok {
		t.Fatal("a TCP-only listener must not block the same UDP port")
	}
}
