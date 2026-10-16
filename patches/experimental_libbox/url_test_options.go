// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0
//
// gomobile-facing wrapper for the daemon's URLTestWithOptions.
//
// Injected by Asterisk4Magisk/AndroidLibBoxLite into the staged sing-box
// source so the AAR bind output gains CommandClient.urlTestWithOptions
// without touching reF1nd/sing-box. See daemon/url_test_options.go for the
// options wire format (protobuf unknown fields on the stock URLTest
// request).

package libbox

import (
	"context"

	"github.com/sagernet/sing-box/daemon"
	E "github.com/sagernet/sing/common/exceptions"
	"google.golang.org/protobuf/types/known/emptypb"
)

// URLTestWithOptions measures outboundTag with husi's custom URL test
// options. It works in both run modes, through the same CommandClient the
// plain URLTest uses: the in-process CommandServer socket in VPN mode and
// the CLI process' api service in root mode, because both register the same
// StartedService gRPC surface.
//
// The measurement is synchronous — the call returns after the daemon stored
// the result in its URL test history, which the group subscription then
// pushes to the UI. Callers are expected to bound concurrency themselves
// (the delay-test worker pool does), matching husi's per-item awaits.
//
// gomobile-bind exposes this as CommandClient.urlTestWithOptions, throwing
// on failure.
func (c *CommandClient) URLTestWithOptions(
	outboundTag string,
	link string,
	timeoutMs int32,
	unifiedDelay bool,
	ignoreHandshakeTime bool,
) error {
	var flags daemon.URLTestFlags
	if unifiedDelay {
		flags |= daemon.URLTestUnifiedDelay
	}
	if ignoreHandshakeTime {
		flags |= daemon.URLTestIgnoreHandshakeTime
	}
	request := &daemon.URLTestRequest{OutboundTag: outboundTag}
	daemon.AppendURLTestOptions(request, link, timeoutMs, flags)
	_, err := callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (*emptypb.Empty, error) {
		return client.URLTest(ctx, request)
	})
	if err != nil {
		return E.Cause(err, "url test with options")
	}
	return nil
}
