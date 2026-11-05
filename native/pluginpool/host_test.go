//go:build linux

package pluginpool

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as the plugin: PLUGINPOOL_TEST_MODE selects a role.
func TestMain(m *testing.M) {
	if mode := os.Getenv("PLUGINPOOL_TEST_MODE"); mode != "" {
		os.Exit(runPlugin(mode))
	}
	fatalWindow = 200 * time.Millisecond
	stableWindow = time.Second
	minRestartDelay = 20 * time.Millisecond
	maxRestartDelay = 80 * time.Millisecond
	stopGrace = 200 * time.Millisecond
	os.Exit(m.Run())
}

func runPlugin(mode string) int {
	if config := os.Getenv("PLUGIN_CONFIG"); config != "" {
		content, err := os.ReadFile(config)
		if err != nil {
			return 2
		}
		fmt.Printf("config %s\n", content)
	}
	switch mode {
	case "exit":
		return 3
	case "crash":
		fmt.Println("running")
		time.Sleep(300 * time.Millisecond)
		return 4
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
	case "protect":
		fmt.Println("reply", protectOnce(os.Getenv("PLUGIN_PROTECT")))
	}
	fmt.Println("running")
	// Not select{}: the runtime would report a deadlock and exit.
	for {
		time.Sleep(time.Hour)
	}
}

func protectOnce(path string) string {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return "dial " + err.Error()
	}
	defer conn.Close()
	socket, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return err.Error()
	}
	defer syscall.Close(socket)
	_, _, err = conn.(*net.UnixConn).WriteMsgUnix([]byte{0}, syscall.UnixRights(socket), nil)
	if err != nil {
		return err.Error()
	}
	reply := make([]byte, 1)
	if _, err = conn.Read(reply); err != nil {
		return err.Error()
	}
	return fmt.Sprint(reply[0])
}

type testLogger struct {
	access sync.Mutex
	lines  []string
}

func (l *testLogger) record(args []any) {
	l.access.Lock()
	l.lines = append(l.lines, fmt.Sprint(args...))
	l.access.Unlock()
}

func (l *testLogger) Info(args ...any)  { l.record(args) }
func (l *testLogger) Warn(args ...any)  { l.record(args) }
func (l *testLogger) Error(args ...any) { l.record(args) }

func (l *testLogger) count(text string) int {
	l.access.Lock()
	defer l.access.Unlock()
	matched := 0
	for _, line := range l.lines {
		if strings.Contains(line, text) {
			matched++
		}
	}
	return matched
}

func (l *testLogger) waitFor(t *testing.T, text string, times int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for l.count(text) < times {
		if time.Now().After(deadline) {
			l.access.Lock()
			defer l.access.Unlock()
			t.Fatalf("no %d×%q in log:\n%s", times, text, strings.Join(l.lines, "\n"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func testSpec(t *testing.T, name, mode string, files ...File) Spec {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{"PLUGINPOOL_TEST_MODE": mode}
	if len(files) > 0 {
		environment["PLUGIN_CONFIG"] = "${file:" + files[0].Name + "}"
	}
	return Spec{Name: name, Command: []string{executable}, Environment: environment, Files: files}
}

func pidOf(h *Host, name string) int {
	h.access.Lock()
	defer h.access.Unlock()
	for _, guard := range h.guards {
		if guard.name == name {
			guard.access.Lock()
			defer guard.access.Unlock()
			if guard.process != nil {
				return guard.process.Pid
			}
		}
	}
	return 0
}

func alive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func TestReloadKeepsUnchangedPlugins(t *testing.T) {
	logger := &testLogger{}
	host := NewHost(logger, nil, func(err error) { t.Errorf("fatal: %v", err) })
	defer host.Close()
	work := t.TempDir()
	stable := testSpec(t, "stable", "run", File{Name: "stable.json", Content: "one"})
	options := &Options{WorkingDir: work, Plugins: []Spec{stable, testSpec(t, "old", "run", File{Name: "old.json"})}}
	if err := host.Apply(options); err != nil {
		t.Fatal(err)
	}
	logger.waitFor(t, "[stable] config one", 1)
	if info, err := os.Stat(filepath.Join(work, "stable.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("plugin file: %v, %v", info, err)
	}
	stablePID, oldPID := pidOf(host, "stable"), pidOf(host, "old")
	options = &Options{WorkingDir: work, Plugins: []Spec{stable, testSpec(t, "new", "run")}}
	if err := host.Apply(options); err != nil {
		t.Fatal(err)
	}
	if pidOf(host, "stable") != stablePID || alive(oldPID) || pidOf(host, "new") == 0 {
		t.Fatal("reload did not keep the unchanged plugin and replace the other")
	}
	if _, err := os.Stat(filepath.Join(work, "old.json")); !os.IsNotExist(err) {
		t.Fatal("removed plugin's file left behind")
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if alive(stablePID) {
		t.Fatal("plugin survived Close")
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Fatalf("files left behind: %v", entries)
	}
	if err := host.Apply(options); err != errClosed {
		t.Fatalf("closed host applied: %v", err)
	}
}

func TestStartupExitIsFatal(t *testing.T) {
	fatal := make(chan error, 1)
	host := NewHost(&testLogger{}, nil, func(err error) { fatal <- err })
	defer host.Close()
	if err := host.Apply(&Options{WorkingDir: t.TempDir(), Plugins: []Spec{testSpec(t, "broken", "exit")}}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-fatal:
		if !strings.Contains(err.Error(), "broken") {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startup exit not reported")
	}
}

func TestMissingFileReferenceFailsStart(t *testing.T) {
	host := NewHost(&testLogger{}, nil, nil)
	defer host.Close()
	spec := testSpec(t, "broken", "run")
	spec.Environment["PLUGIN_CONFIG"] = "${file:missing.json}"
	if err := host.Apply(&Options{WorkingDir: t.TempDir(), Plugins: []Spec{spec}}); err == nil {
		t.Fatal("unknown file reference accepted")
	}
}

func TestCrashRestartsWithBackoff(t *testing.T) {
	logger := &testLogger{}
	host := NewHost(logger, nil, func(err error) { t.Errorf("fatal: %v", err) })
	defer host.Close()
	if err := host.Apply(&Options{WorkingDir: t.TempDir(), Plugins: []Spec{testSpec(t, "crashy", "crash")}}); err != nil {
		t.Fatal(err)
	}
	logger.waitFor(t, "[crashy] running", 3)
	logger.waitFor(t, "restarting in 40ms", 1)
}

func TestStopKillsPluginIgnoringTerm(t *testing.T) {
	logger := &testLogger{}
	host := NewHost(logger, nil, nil)
	if err := host.Apply(&Options{WorkingDir: t.TempDir(), Plugins: []Spec{testSpec(t, "stubborn", "ignore-term")}}); err != nil {
		t.Fatal(err)
	}
	logger.waitFor(t, "[stubborn] running", 1)
	pid := pidOf(host, "stubborn")
	began := time.Now()
	_ = host.Close()
	if alive(pid) || time.Since(began) > 2*time.Second {
		t.Fatalf("stop took %v, alive=%v", time.Since(began), alive(pid))
	}
}

// PDEATHSIG fires when the forking thread exits; a JNI caller's thread can.
func TestPluginOutlivesCallingThread(t *testing.T) {
	logger := &testLogger{}
	host := NewHost(logger, nil, nil)
	defer host.Close()
	applied := make(chan error, 1)
	go func() {
		// Exiting while locked terminates this thread.
		runtime.LockOSThread()
		applied <- host.Apply(&Options{WorkingDir: t.TempDir(), Plugins: []Spec{testSpec(t, "child", "run")}})
	}()
	if err := <-applied; err != nil {
		t.Fatal(err)
	}
	logger.waitFor(t, "[child] running", 1)
	pid := pidOf(host, "child")
	time.Sleep(300 * time.Millisecond)
	if !alive(pid) || pidOf(host, "child") != pid || logger.count("exited") != 0 {
		t.Fatal("plugin died with the thread that applied it")
	}
}

func TestProtectSurvivesReload(t *testing.T) {
	logger := &testLogger{}
	protected := make(chan int, 4)
	host := NewHost(logger, func(fd int) error {
		var stat syscall.Stat_t
		if err := syscall.Fstat(fd, &stat); err != nil {
			return err
		}
		protected <- fd
		return nil
	}, nil)
	defer host.Close()
	work := t.TempDir()
	path := filepath.Join(work, "protect.sock")
	plugin := func(name string) Spec {
		spec := testSpec(t, name, "protect")
		spec.Environment["PLUGIN_PROTECT"] = path
		return spec
	}
	if err := host.Apply(&Options{WorkingDir: work, ProtectPath: path, VerifyPeer: true, Plugins: []Spec{plugin("first")}}); err != nil {
		t.Fatal(err)
	}
	logger.waitFor(t, "[first] reply 1", 1)
	// A reload that replaces another plugin must not unlink the shared socket.
	if err := host.Apply(&Options{WorkingDir: work, ProtectPath: path, VerifyPeer: true,
		Plugins: []Spec{plugin("first"), plugin("second")}}); err != nil {
		t.Fatal(err)
	}
	logger.waitFor(t, "[second] reply 1", 1)
	if len(protected) != 2 {
		t.Fatalf("protector saw %d sockets", len(protected))
	}
	_ = host.Close()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("protect socket left behind")
	}
}

func TestProtectRequiresProtector(t *testing.T) {
	host := NewHost(&testLogger{}, nil, nil)
	defer host.Close()
	work := t.TempDir()
	err := host.Apply(&Options{WorkingDir: work, ProtectPath: filepath.Join(work, "protect.sock"),
		Plugins: []Spec{testSpec(t, "plugin", "run")}})
	if err == nil {
		t.Fatal("protect path accepted without a protector")
	}
}

func TestDescends(t *testing.T) {
	if !descends(os.Getpid(), os.Getpid()) || !descends(os.Getpid(), os.Getppid()) || descends(1, os.Getpid()) {
		t.Fatal("ancestry walk")
	}
}
