// Copyright 2026, Asterisk4Magisk contributors
// SPDX-License-Identifier: LGPL-3.0

package process

import (
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-tun"
)

func CompleteFlowOwner(info *adapter.ConnectionOwner, packages tun.PackageManager) {
	if len(info.PackageNames) == 0 {
		completeProcessInfo(info, packages)
	}
}
