// Copyright (C) 2026 AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0

//go:build linux || android

package daemon

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"

	E "github.com/sagernet/sing/common/exceptions"
	"golang.org/x/sys/unix"
)

// verifyPluginPeer accepts a request only when the connecting process descends
// from this core process. Same-UID alone would also accept unrelated apps that
// share the socket path.
func verifyPluginPeer(conn *net.UnixConn) error {
	processID, err := pluginPeerProcessID(conn)
	if err != nil {
		return E.Cause(err, "identify protect peer")
	}
	descendant, err := isPluginDescendant(processID, os.Getpid())
	if err != nil {
		return E.Cause(err, "trace ancestors of protect peer ", processID)
	}
	if !descendant {
		return E.New("protect peer ", processID, " is not a plugin")
	}
	return nil
}

func pluginPeerProcessID(conn *net.UnixConn) (int, error) {
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credentials *unix.Ucred
	var credentialsErr error
	err = rawConn.Control(func(fd uintptr) {
		credentials, credentialsErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err != nil {
		return 0, err
	}
	if credentialsErr != nil {
		return 0, credentialsErr
	}
	return int(credentials.Pid), nil
}

func isPluginDescendant(processID int, ancestorID int) (bool, error) {
	for {
		if processID == ancestorID {
			return true, nil
		}
		if processID <= 1 {
			return false, nil
		}
		parentID, err := pluginParentProcessID(processID)
		if err != nil {
			return false, err
		}
		processID = parentID
	}
}

func pluginParentProcessID(processID int) (int, error) {
	content, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(processID), "stat"))
	if err != nil {
		return 0, err
	}
	// The command name is parenthesized and may contain spaces: fields after
	// the last ')' start with state, then ppid.
	commandEnd := bytes.LastIndexByte(content, ')')
	if commandEnd < 0 {
		return 0, E.New("invalid process stat")
	}
	fields := bytes.Fields(content[commandEnd+1:])
	if len(fields) <= 1 {
		return 0, E.New("incomplete process stat")
	}
	return strconv.Atoi(string(fields[1]))
}
