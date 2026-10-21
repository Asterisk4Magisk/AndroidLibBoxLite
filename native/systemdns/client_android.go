//go:build android

package systemdns

import (
	"bufio"
	_ "embed"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	environment "github.com/sagernet/sing-box/common/androidcli"
	"github.com/sagernet/sing-box/dns/transport/local/systemconfig"
)

// Generated in staging by javac/D8, never packaged as an app class.
//
//go:embed helper.dex
var helperDex []byte

var channelFailed atomic.Bool

// ExitCode reports the status for a core that shut down after losing its helper.
func ExitCode() int {
	if channelFailed.Load() {
		return 70
	}
	return 0
}

// decodeSnapshot already normalized and validated every endpoint.
func update(servers []string) {
	endpoints := make([]netip.AddrPort, 0, len(servers))
	for _, server := range servers {
		endpoints = append(endpoints, netip.MustParseAddrPort(server))
	}
	systemconfig.SetManagedServers(endpoints)
}

// StartService is called by the run command after its signal handler exists,
// so a helper failure always takes the graceful shutdown path.
func StartService(home string) func() {
	stop, err := Start(home)
	if err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(70)
	}
	return stop
}

// Start is called only for a running CLI service, never JNI or CLI tool commands.
func Start(home string) (func(), error) {
	if !environment.ManagedSystemDNS() {
		return func() {}, nil
	}
	path, err := ensureHelper(home, helperDex)
	if err != nil {
		return nil, fmt.Errorf("system DNS helper file: %w", err)
	}
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("system DNS socketpair: %w", err)
	}
	parent := os.NewFile(uintptr(pair[0]), "system-dns-parent")
	child := os.NewFile(uintptr(pair[1]), "system-dns-child")
	defer child.Close()
	// FileConn duplicates the parent endpoint with CLOEXEC for Go's poller.
	connection, err := net.FileConn(parent)
	_ = parent.Close()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("/system/bin/app_process", "-Djava.class.path="+path, "/",
		"--nice-name=libbox-system-dns", "libbox.systemdns.SystemDnsHelper", "3")
	// The same socket supplies stdin's lifeline and fd 3's outgoing snapshots.
	// Only ExtraFiles/stdio duplicates survive exec; parent fds stay CLOEXEC.
	cmd.Stdin = child
	cmd.ExtraFiles = []*os.File{child}
	// stdout only carries the standalone smoke test's readiness line.
	cmd.Stderr = os.Stderr
	// Keep helper signals separate from the core's graceful group shutdown.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	started, done := make(chan error, 1), make(chan error, 1)
	go func() {
		// Linux PDEATHSIG watches the creating thread. Pin that thread for the
		// entire child lifetime so Go cannot retire it while the core is alive.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		err := cmd.Start()
		started <- err
		if err == nil {
			done <- cmd.Wait()
		}
	}()
	if err = <-started; err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("start system DNS helper: %w", err)
	}
	_ = child.Close()
	stopping := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			close(stopping)
			_ = connection.Close() // EOF on helper stdin also covers core crashes.
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		})
	}
	_ = connection.SetReadDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReaderSize(connection, MaxFrame)
	read := func() ([]string, error) {
		frame, err := reader.ReadSlice('\n')
		if err != nil {
			return nil, err
		}
		return decodeSnapshot(frame)
	}
	servers, err := read()
	if err != nil {
		stop()
		return nil, fmt.Errorf("system DNS initial snapshot: %w", err)
	}
	update(servers)
	_ = connection.SetReadDeadline(time.Time{})
	go func() {
		previous := servers
		for {
			next, err := read()
			if err != nil {
				select {
				case <-stopping:
					return
				default:
				}
				update(nil)
				_, _ = fmt.Fprintf(os.Stderr, "system DNS channel failed: %v\n", err)
				// Shut down like an external SIGTERM so inbounds remove their
				// routes and redirect rules; force the status if that stalls.
				channelFailed.Store(true)
				_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
				time.AfterFunc(10*time.Second, func() { os.Exit(70) })
				return
			}
			if !reflect.DeepEqual(previous, next) {
				previous = next
				update(next)
			}
		}
	}()
	return stop, nil
}
