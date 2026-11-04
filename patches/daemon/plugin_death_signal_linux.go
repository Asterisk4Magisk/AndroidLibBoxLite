// Copyright (C) 2026 AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0

//go:build linux || android

package daemon

import "syscall"

// pluginDeathSignal makes the kernel terminate a plugin when the core dies,
// however it dies (SIGKILL included).
func pluginDeathSignal() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
