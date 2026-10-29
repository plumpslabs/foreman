package main

import (
	"net"
	"strconv"
	"testing"
)

func TestListenLocalSelectsAvailablePort(t *testing.T) {
	listener, port, err := listenLocal(0)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if port == 0 || listener.Addr().String() == "" {
		t.Fatalf("invalid listener address %q, port %d", listener.Addr(), port)
	}
	if listener.Addr().(*net.TCPAddr).IP.String() != "127.0.0.1" {
		t.Fatalf("listener bound to non-loopback address %s", listener.Addr())
	}
}

func TestListenLocalFallsBackWhenPreferredPortOccupied(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	preferred := occupied.Addr().(*net.TCPAddr).Port

	listener, port, err := listenLocal(preferred)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if port == preferred {
		t.Fatalf("selected occupied port %s", strconv.Itoa(port))
	}
	if listener.Addr().(*net.TCPAddr).IP.String() != "127.0.0.1" {
		t.Fatalf("listener bound to non-loopback address %s", listener.Addr())
	}
}
