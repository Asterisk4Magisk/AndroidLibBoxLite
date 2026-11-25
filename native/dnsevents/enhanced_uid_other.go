// Copyright 2026, Asterisk4Magisk contributors
// SPDX-License-Identifier: LGPL-3.0

//go:build !linux && !android

package dnsevents

import (
	"math"
	"net/netip"
)

const UnknownEnhancedUID = math.MaxUint32

func setEnhancedUIDObservation(bool) {}

func EnhancedUID(string, netip.AddrPort, netip.AddrPort, []byte) (uint32, bool) {
	return 0, false
}
