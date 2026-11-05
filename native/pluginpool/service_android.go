//go:build android

package pluginpool

import (
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sagernet/sing-box/log"
)

var failed atomic.Bool

// ExitCode reports the status for a core that shut down after a plugin failed.
func ExitCode() int {
	if failed.Load() {
		return 70
	}
	return 0
}

type boxLogger struct{}

func (boxLogger) Info(args ...any)  { log.Info(args...) }
func (boxLogger) Warn(args ...any)  { log.Warn(args...) }
func (boxLogger) Error(args ...any) { log.Error(args...) }

// StartService is called by the run command after its signal handler exists,
// so a plugin failure always takes the graceful shutdown path. ROOT mode
// cannot protect sockets: the app maps plugin traffic through the core.
func StartService(configPaths []string) func() {
	options, err := ReadEnvelope(configPaths)
	if err == nil && options == nil {
		return func() {}
	}
	var host *Host
	if err == nil {
		host = NewHost(boxLogger{}, nil, shutdown)
		err = host.Apply(options)
	}
	if err != nil {
		_, _ = os.Stderr.WriteString("plugin: " + err.Error() + "\n")
		os.Exit(70)
	}
	return func() { _ = host.Close() }
}

// shutdown stops the core like an external SIGTERM, so inbounds remove their
// routes, and forces the status if that stalls.
func shutdown(error) {
	if failed.Swap(true) {
		return
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	time.AfterFunc(10*time.Second, func() { os.Exit(70) })
}
