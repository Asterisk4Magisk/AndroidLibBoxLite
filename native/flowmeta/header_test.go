// Copyright 2026, Asterisk4Magisk contributors
// SPDX-License-Identifier: LGPL-3.0

package flowmeta

import (
	"encoding/hex"
	"io"
	"net/netip"
	"testing"
)

const testToken = "0123456789abcdef0123456789abcdef"

func testHeader(udp bool) Header {
	return Header{Source: netip.MustParseAddrPort("172.19.0.1:35000"), Destination: netip.MustParseAddrPort("198.18.0.9:443"), UDP: udp, UID: 1012345}
}

func TestHeader(t *testing.T) {
	for _, udp := range []bool{false, true} {
		for _, ipv6 := range []bool{false, true} {
			h := testHeader(udp)
			if ipv6 {
				h.Source = netip.MustParseAddrPort("[fdfe:dcba::1]:35000")
				h.Destination = netip.MustParseAddrPort("[2001:db8::9]:443")
			}
			b, err := h.Marshal(testToken)
			if err != nil {
				t.Fatal(err)
			}
			got, n, err := Parse(append(b, 1, 2), testToken, udp)
			if err != nil || got != h || n != len(b) {
				t.Fatalf("round trip: %#v %d %v", got, n, err)
			}
			if _, _, err = Parse(b, "ffffffffffffffffffffffffffffffff", udp); err == nil {
				t.Fatal("accepted wrong credential")
			}
			if _, _, err = Parse(b, testToken, !udp); err == nil {
				t.Fatal("accepted wrong protocol")
			}
			for n := 0; n < len(b); n++ {
				if _, _, err = Parse(b[:n], testToken, udp); err == nil {
					t.Fatalf("accepted truncated header at %d", n)
				}
			}
		}
	}
}

func TestFragmentedTCPPreservesPayload(t *testing.T) {
	h := testHeader(false)
	prefix, _ := h.Marshal(testToken)
	reader, writer := io.Pipe()
	go func() {
		defer writer.Close()
		for _, b := range prefix {
			_, _ = writer.Write([]byte{b})
		}
		_, _ = writer.Write([]byte("payload"))
	}()
	got, err := Read(reader, testToken)
	if err != nil || got != h {
		t.Fatalf("header: %#v %v", got, err)
	}
	payload, err := io.ReadAll(reader)
	if err != nil || string(payload) != "payload" {
		t.Fatalf("payload: %q %v", payload, err)
	}
}

// Stable C/Rust interoperability vector, including a full multi-user Android UID.
func TestWireVector(t *testing.T) {
	b, _ := testHeader(true).Marshal(testToken)
	const expected = "0d0a0d0a000d0a515549540a21120034ac130001c612000988b801bbe00025013031323334353637383961626364656630313233343536373839616263646566000f7279"
	if hex.EncodeToString(b) != expected {
		t.Fatalf("wire vector: %x", b)
	}
}
