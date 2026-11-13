// Copyright 2026, Asterisk4Magisk contributors
// SPDX-License-Identifier: LGPL-3.0

//go:build !linux || !cgo

package flowmeta

import (
	"errors"
	"net/netip"
)

func FindSocket(string, netip.AddrPort, netip.AddrPort) (uint32, uint32, error) {
	return 0, 0, errors.New("socket diag unavailable")
}
