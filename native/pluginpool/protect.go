//go:build linux

package pluginpool

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	protectTimeout = 5 * time.Second
	// Bounds the /proc walk for a peer that is not a direct plugin child.
	maxAncestry = 32
)

// protectServer speaks the protect-go protocol: a plugin sends one socket in
// an SCM_RIGHTS message and reads one byte back (1 protected, 0 failed).
type protectServer struct {
	path     string
	host     *Host
	listener *net.UnixListener
	verify   atomic.Bool
}

func listenProtect(path string, verify bool, host *Host) (*protectServer, error) {
	// A killed core leaves its socket behind; never replace anything else.
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode().Type() != os.ModeSocket {
			return nil, fmt.Errorf("plugin protect path is not a socket: %s", path)
		}
		err = os.Remove(path)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("replace plugin protect socket: %w", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen plugin protect socket: %w", err)
	}
	// Plugins run under the core's UID.
	if err = os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("chmod plugin protect socket: %w", err)
	}
	server := &protectServer{path: path, host: host, listener: listener}
	server.verify.Store(verify)
	go server.serve()
	return server, nil
}

// close unlinks the path, so it runs only when the path is no longer used.
func (s *protectServer) close() {
	_ = s.listener.Close()
}

func (s *protectServer) serve() {
	for {
		conn, err := s.listener.AcceptUnix()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			// Resource exhaustion; do not spin.
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go s.handle(conn)
	}
}

func (s *protectServer) handle(conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(protectTimeout))
	reply := byte(1)
	if err := s.protect(conn); err != nil {
		reply = 0
		s.host.logger.Error("plugin protect request rejected: ", err)
	}
	_, _ = conn.Write([]byte{reply})
}

func (s *protectServer) protect(conn *net.UnixConn) error {
	if s.verify.Load() {
		if err := s.verifyPeer(conn); err != nil {
			return err
		}
	}
	fd, err := receiveSocket(conn)
	if err != nil {
		return err
	}
	// The raw descriptor: os.File.Fd would clear O_NONBLOCK on a description
	// the plugin shares.
	defer syscall.Close(fd)
	return s.host.protect(fd)
}

func receiveSocket(conn *net.UnixConn) (int, error) {
	var payload [1]byte
	oob := make([]byte, syscall.CmsgSpace(4))
	// Go requests MSG_CMSG_CLOEXEC, so a concurrent plugin fork cannot inherit it.
	_, oobn, flags, _, err := conn.ReadMsgUnix(payload[:], oob)
	if err != nil {
		return -1, fmt.Errorf("read protect request: %w", err)
	}
	messages, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return -1, fmt.Errorf("parse protect request: %w", err)
	}
	var fds []int
	for index := range messages {
		rights, err := syscall.ParseUnixRights(&messages[index])
		if err == nil {
			fds = append(fds, rights...)
		}
	}
	if len(fds) != 1 || flags&syscall.MSG_CTRUNC != 0 {
		for _, fd := range fds {
			_ = syscall.Close(fd)
		}
		return -1, errors.New("protect request must carry exactly one socket")
	}
	return fds[0], nil
}

// verifyPeer accepts only the core's descendants: other processes of the
// same UID could otherwise reach the socket path.
func (s *protectServer) verifyPeer(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var credentials *syscall.Ucred
	var credentialsErr error
	err = raw.Control(func(fd uintptr) {
		credentials, credentialsErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err == nil {
		err = credentialsErr
	}
	if err != nil {
		return fmt.Errorf("protect peer credentials: %w", err)
	}
	pid := int(credentials.Pid)
	if s.host.isChild(pid) || descends(pid, os.Getpid()) {
		return nil
	}
	return fmt.Errorf("protect peer %d is not a plugin", pid)
}

func descends(pid, ancestor int) bool {
	for range maxAncestry {
		if pid == ancestor {
			return true
		}
		if pid <= 1 {
			return false
		}
		parent, err := parentOf(pid)
		if err != nil {
			return false
		}
		pid = parent
	}
	return false
}

func parentOf(pid int) (int, error) {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	// comm is parenthesized and may hold spaces: state and ppid follow the last ')'.
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, errors.New("malformed process stat")
	}
	fields := bytes.Fields(stat[end+1:])
	if len(fields) < 2 {
		return 0, errors.New("malformed process stat")
	}
	return strconv.Atoi(string(fields[1]))
}
