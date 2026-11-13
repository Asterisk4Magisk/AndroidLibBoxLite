// Copyright 2026, Asterisk4Magisk contributors
// SPDX-License-Identifier: LGPL-3.0

package process

import (
	"errors"
	"net/netip"
	"syscall"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/androidcli/flowmeta"
	"github.com/sagernet/sing-tun"
)

func resolveFlowSocket(network string, source, destination netip.AddrPort) (inode, uid uint32, err error) {
	uid, inode, err = flowmeta.FindSocket(network, source, destination)
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ESRCH) {
		err = ErrNotFound
	}
	return
}

func CompleteFlowOwner(info *adapter.ConnectionOwner, packages tun.PackageManager) {
	if len(info.PackageNames) == 0 {
		completeProcessInfo(info, packages)
	}
}
