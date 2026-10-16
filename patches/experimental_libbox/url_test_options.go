// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0
//
// gomobile-facing wrapper for the daemon's URLTestWithOptions.
//
// Injected by Asterisk4Magisk/AndroidLibBoxLite into the staged sing-box
// source so the bind output gains CommandServer.urlTestWithOptions /
// CommandServer.urlTestDefaults without touching reF1nd/sing-box.

package libbox

import (
	"github.com/sagernet/sing-box/daemon"
)

// URLTestWithOptions measures outboundTag through the running instance with
// husi's custom measurement flags. Returns the latency in milliseconds, or
// -1 when the service is not started or the outbound is missing.
//
// flags is the bitmask from daemon.URLTestFlags: 1 = unified delay,
// 2 = ignore handshake time. The Kotlin side passes 0 to express "both
// disabled" without importing the type.
//
// gomobile-bind exposes this as CommandServer.urlTestWithOptions.
func (s *CommandServer) URLTestWithOptions(
	outboundTag string,
	link string,
	timeoutMs int32,
	unifiedDelay bool,
	ignoreHandshakeTime bool,
) int32 {
	var flags daemon.URLTestFlags
	if unifiedDelay {
		flags |= daemon.URLTestUnifiedDelay
	}
	if ignoreHandshakeTime {
		flags |= daemon.URLTestIgnoreHandshakeTime
	}
	latency, err := s.StartedService.URLTestWithOptions(outboundTag, link, timeoutMs, flags)
	if err != nil {
		return -1
	}
	return latency
}

// URLTestDefaultLink exposes the default test link so the Kotlin side can
// pre-fill the settings field with the same constant.
func (s *CommandServer) URLTestDefaultLink() string {
	return daemon.UrlTestDefaultLink
}
