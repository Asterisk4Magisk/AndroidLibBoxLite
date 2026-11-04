// Copyright (C) 2026 AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0

//go:build !linux && !android

package daemon

import "syscall"

func pluginDeathSignal() *syscall.SysProcAttr { return nil }
