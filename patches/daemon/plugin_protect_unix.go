// Copyright (C) 2026 AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0
//
// Unix socket server that receives one socket from a plugin process and hands
// it to the platform protector, so the plugin's own dials stay outside the
// tunnel. Wire format matches github.com/xchacha20-poly1305/protect-go: one
// SCM_RIGHTS message carrying the socket, one reply byte (1 = success).

//go:build !windows

package daemon

import (
	"net"
	"os"
	"sync"
	"time"

	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
	"golang.org/x/sys/unix"
)

const (
	pluginProtectReplySuccess = byte(1)
	pluginProtectReplyFailure = byte(0)
	pluginProtectTimeout      = 5 * time.Second
)

type pluginProtectServer struct {
	listener *net.UnixListener
	protect  PluginProtector
	verify   bool

	access sync.Mutex
	closed bool
}

func startPluginProtectServer(path string, verifyPeer bool, protector PluginProtector) (*pluginProtectServer, error) {
	info, err := os.Lstat(path)
	switch {
	case err == nil && info.Mode().Type() == os.ModeSocket:
		if err = os.Remove(path); err != nil {
			return nil, E.Cause(err, "remove stale protect socket")
		}
	case err == nil:
		return nil, E.New(path, " exists and is not a socket")
	case !os.IsNotExist(err):
		return nil, E.Cause(err, "inspect protect socket")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, E.Cause(err, "listen protect socket")
	}
	// The plugin runs as a child process of this one, but keep the socket
	// connectable for the multi-user case too (same policy as protect-go).
	if err = os.Chmod(path, 0o777); err != nil {
		_ = listener.Close()
		return nil, E.Cause(err, "chmod protect socket")
	}
	server := &pluginProtectServer{
		listener: listener,
		protect:  protector,
		verify:   verifyPeer,
	}
	go server.serve()
	return server, nil
}

func (s *pluginProtectServer) serve() {
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *pluginProtectServer) handle(conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(pluginProtectTimeout))
	reply := pluginProtectReplySuccess
	if err := s.handleRequest(conn); err != nil {
		reply = pluginProtectReplyFailure
		// A rejected or failed request must stay visible in the core log.
		log.Error("plugin protect request rejected: ", err)
	}
	_, _ = conn.Write([]byte{reply})
}

func (s *pluginProtectServer) handleRequest(conn *net.UnixConn) error {
	file, err := receivePluginSocket(conn)
	if err != nil {
		return err
	}
	defer file.Close()
	if s.verify {
		if err = verifyPluginPeer(conn); err != nil {
			return err
		}
	}
	return s.protect(int(file.Fd()))
}

func (s *pluginProtectServer) Close() error {
	s.access.Lock()
	if s.closed {
		s.access.Unlock()
		return nil
	}
	s.closed = true
	s.access.Unlock()
	err := s.listener.Close()
	if err != nil {
		return E.Cause(err, "close protect listener")
	}
	return nil
}

func receivePluginSocket(conn *net.UnixConn) (*os.File, error) {
	payload := make([]byte, 1)
	oob := make([]byte, unix.CmsgSpace(4))
	_, oobn, _, _, err := conn.ReadMsgUnix(payload, oob)
	if err != nil {
		return nil, E.Cause(err, "read protect request")
	}
	controlMessages, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return nil, E.Cause(err, "parse protect control message")
	}
	var fds []int
	for index := range controlMessages {
		rights, rightsErr := unix.ParseUnixRights(&controlMessages[index])
		if rightsErr != nil {
			closePluginFiles(fds)
			return nil, E.Cause(rightsErr, "parse protect rights")
		}
		fds = append(fds, rights...)
	}
	if len(fds) != 1 {
		closePluginFiles(fds)
		return nil, E.New("protect request must carry exactly one socket")
	}
	file := os.NewFile(uintptr(fds[0]), "plugin-protect")
	if file == nil {
		return nil, E.New("invalid protect socket")
	}
	return file, nil
}

func closePluginFiles(fds []int) {
	for _, fd := range fds {
		_ = unix.Close(fd)
	}
}
