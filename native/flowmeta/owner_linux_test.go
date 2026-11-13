// Copyright 2026, Asterisk4Magisk contributors
// SPDX-License-Identifier: LGPL-3.0

//go:build linux && cgo

package flowmeta

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"testing"
)

func TestSocketDiagLiveTCP(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	source := client.LocalAddr().(*net.TCPAddr).AddrPort()
	destination := client.RemoteAddr().(*net.TCPAddr).AddrPort()
	for range 20 {
		uid, inode, err := FindSocket("tcp", source, destination)
		if err != nil || uid != uint32(os.Getuid()) || inode == 0 {
			t.Fatalf("uid=%d inode=%d err=%v", uid, inode, err)
		}
	}
	_, _, err = FindSocket("tcp", source, netip.AddrPortFrom(destination.Addr(), 1))
	if err == nil {
		t.Fatal("wrong destination attributed")
	}
}

func TestSocketDiagLiveUDP(t *testing.T) {
	for _, address := range []string{"0.0.0.0:0", "[::]:0", "127.0.0.1:0", "[::1]:0"} {
		t.Run(address, func(t *testing.T) {
			listener, err := net.ListenPacket("udp", address)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			local := listener.LocalAddr().(*net.UDPAddr).AddrPort()
			ip := netip.MustParseAddr("127.0.0.1")
			if local.Addr().Is6() {
				ip = netip.IPv6Loopback()
			}
			source := netip.AddrPortFrom(ip, local.Port())
			uid, inode, err := FindSocket("udp", source, netip.AddrPortFrom(ip, 443))
			if err != nil || uid != uint32(os.Getuid()) || inode == 0 {
				t.Fatalf("uid=%d inode=%d err=%v", uid, inode, err)
			}
		})
	}
}

func TestSocketDiagConnectedUDP(t *testing.T) {
	for _, address := range []string{"127.0.0.1:443", "[::1]:443"} {
		client, err := net.Dial("udp", address)
		if err != nil {
			t.Fatal(err)
		}
		source := client.LocalAddr().(*net.UDPAddr).AddrPort()
		destination := client.RemoteAddr().(*net.UDPAddr).AddrPort()
		for _, target := range []netip.AddrPort{destination, {}} {
			uid, inode, err := FindSocket("udp", source, target)
			if err != nil || uid != uint32(os.Getuid()) || inode == 0 {
				t.Errorf("%s -> %s: uid=%d inode=%d err=%v", source, target, uid, inode, err)
			}
		}
		client.Close()
	}
}

func TestSocketDiagAbsentIsFinal(t *testing.T) {
	_, _, err := FindSocket("tcp", netip.MustParseAddrPort("127.0.0.1:1"), netip.MustParseAddrPort("127.0.0.1:2"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected absent socket: %v", err)
	}
}
