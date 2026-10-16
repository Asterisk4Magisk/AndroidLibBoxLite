// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0
//
// Round-trip tests for the URL test options unknown-field envelope
// (AppendURLTestOptions / urlTestOptionsFromRequest in url_test_options.go).
// The envelope is the entire contract between the libbox client wrapper and
// the patched daemon handler, so it is pinned here.

package daemon

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestURLTestOptionsRoundTrip(t *testing.T) {
	request := &URLTestRequest{OutboundTag: "proxy-a"}
	AppendURLTestOptions(request, "http://cp.cloudflare.com/", 5000, URLTestUnifiedDelay|URLTestIgnoreHandshakeTime)
	wire, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded := &URLTestRequest{}
	if err := proto.Unmarshal(wire, decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.OutboundTag != "proxy-a" {
		t.Fatalf("outbound tag lost: %q", decoded.OutboundTag)
	}
	options, loaded := urlTestOptionsFromRequest(decoded)
	if !loaded {
		t.Fatal("options envelope not detected after round trip")
	}
	if options.Link != "http://cp.cloudflare.com/" {
		t.Fatalf("link mismatch: %q", options.Link)
	}
	if options.TimeoutMs != 5000 {
		t.Fatalf("timeout mismatch: %d", options.TimeoutMs)
	}
	if options.Flags != URLTestUnifiedDelay|URLTestIgnoreHandshakeTime {
		t.Fatalf("flags mismatch: %d", options.Flags)
	}
}

func TestURLTestOptionsZeroValuesRoundTrip(t *testing.T) {
	// The Kotlin side always sends the envelope, even with both toggles off
	// and the link left at its default; the flags field alone marks it.
	request := &URLTestRequest{OutboundTag: "proxy-b"}
	AppendURLTestOptions(request, "", 0, 0)
	wire, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded := &URLTestRequest{}
	if err := proto.Unmarshal(wire, decoded); err != nil {
		t.Fatal(err)
	}
	options, loaded := urlTestOptionsFromRequest(decoded)
	if !loaded {
		t.Fatal("options envelope not detected after round trip")
	}
	if options.Link != "" || options.TimeoutMs != 0 || options.Flags != 0 {
		t.Fatalf("options mismatch: %+v", options)
	}
}

func TestURLTestOptionsAbsentOnStockRequest(t *testing.T) {
	request := &URLTestRequest{OutboundTag: "proxy-c"}
	if _, loaded := urlTestOptionsFromRequest(request); loaded {
		t.Fatal("stock request must not be detected as an options request")
	}
	if _, loaded := urlTestOptionsFromRequest(&URLTestRequest{}); loaded {
		t.Fatal("empty request must not be detected as an options request")
	}
}
