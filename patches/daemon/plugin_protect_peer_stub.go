// Copyright (C) 2026 AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0

//go:build !linux && !android

package daemon

import "net"

// verifyPluginPeer is a no-op outside Linux/Android: only the Android build
// runs plugin processes under a shared UID where peer credentials are needed.
func verifyPluginPeer(conn *net.UnixConn) error {
	return nil
}
