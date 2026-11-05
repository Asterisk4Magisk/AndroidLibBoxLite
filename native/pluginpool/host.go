//go:build linux

package pluginpool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// Variables so tests can shorten them.
var (
	// A first run that exits sooner is a configuration error, not a crash.
	fatalWindow = time.Second
	// A run that lasted this long resets the restart delay.
	stableWindow    = 30 * time.Second
	minRestartDelay = time.Second
	maxRestartDelay = 30 * time.Second
	stopGrace       = time.Second
	// Bounds Wait when a plugin's descendants keep its output pipe open.
	pipeGrace = 500 * time.Millisecond
)

// maxLine bounds one logged output line; the rest of a longer line is dropped.
const maxLine = 4096

// Host supervises the plugins of one service. Apply reconciles the running
// set with an envelope, so a reload restarts only the plugins whose spec
// changed and the protect socket outlives reloads.
type Host struct {
	logger  Logger
	protect Protector
	onFatal func(error)
	// Plugin PIDs, so a direct child skips the /proc ancestry walk.
	children sync.Map

	access sync.Mutex
	closed bool
	guards map[string]*guard
	server *protectServer
}

// NewHost returns an empty host. onFatal runs on its own goroutine when a
// plugin cannot be kept running; protector may be nil without a protect path.
func NewHost(logger Logger, protector Protector, onFatal func(error)) *Host {
	return &Host{
		logger:  logger,
		protect: protector,
		onFatal: onFatal,
		guards:  make(map[string]*guard),
	}
}

// Apply makes the running plugins match options; nil stops all of them.
// Unchanged plugins keep running. If any plugin cannot start, every plugin
// is stopped and the error is returned.
func (h *Host) Apply(options *Options) error {
	h.access.Lock()
	defer h.access.Unlock()
	if h.closed {
		return errClosed
	}
	var keys []string
	wanted := make(map[string]bool)
	if options != nil {
		for index := range options.Plugins {
			key := specKey(options.WorkingDir, &options.Plugins[index])
			keys = append(keys, key)
			wanted[key] = true
		}
	}
	// Stop before starting: replaced plugins release their ports and files.
	var removed []*guard
	for key, guard := range h.guards {
		if !wanted[key] || guard.exited() {
			removed = append(removed, guard)
			delete(h.guards, key)
		}
	}
	stopGuards(removed)
	err := h.updateServer(options)
	if err == nil && options != nil {
		err = h.startMissing(options, keys)
	}
	if err != nil {
		h.stopAll()
	}
	return err
}

func (h *Host) startMissing(options *Options, keys []string) error {
	if err := os.MkdirAll(options.WorkingDir, 0o700); err != nil {
		return fmt.Errorf("create plugin working directory: %w", err)
	}
	environment := os.Environ()
	for index, key := range keys {
		if h.guards[key] != nil {
			continue
		}
		guard, err := h.start(options.WorkingDir, &options.Plugins[index], environment)
		if err != nil {
			return err
		}
		h.guards[key] = guard
	}
	return nil
}

// Close stops every plugin and the protect socket. Safe to call twice.
func (h *Host) Close() error {
	h.access.Lock()
	defer h.access.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	h.stopAll()
	return nil
}

func (h *Host) stopAll() {
	guards := make([]*guard, 0, len(h.guards))
	for key, guard := range h.guards {
		guards = append(guards, guard)
		delete(h.guards, key)
	}
	stopGuards(guards)
	if h.server != nil {
		h.server.close()
		h.server = nil
	}
}

// The listener is kept while its path is unchanged: plugins that survive a
// reload keep using it, and closing it would unlink the path.
func (h *Host) updateServer(options *Options) error {
	var path string
	var verify bool
	if options != nil {
		path, verify = options.ProtectPath, options.VerifyPeer
	}
	if h.server != nil && h.server.path == path {
		h.server.verify.Store(verify)
		return nil
	}
	if h.server != nil {
		h.server.close()
		h.server = nil
	}
	if path == "" {
		return nil
	}
	if h.protect == nil {
		return fmt.Errorf("plugin protect path requires a protector")
	}
	server, err := listenProtect(path, verify, h)
	if err != nil {
		return err
	}
	h.server = server
	return nil
}

func (h *Host) isChild(pid int) bool {
	_, loaded := h.children.Load(pid)
	return loaded
}

func (h *Host) fatal(err error) {
	h.logger.Error("plugin failed: ", err)
	if h.onFatal != nil {
		go h.onFatal(err)
	}
}

// specKey identifies a plugin by everything that reaches its process.
func specKey(workingDir string, spec *Spec) string {
	key, _ := json.Marshal(struct {
		Dir  string
		Spec *Spec
	}{workingDir, spec})
	return string(key)
}

type guard struct {
	host   *Host
	name   string
	cmd    []string
	env    []string
	dir    string
	files  []string
	output *lineWriter
	stop   chan struct{}
	done   chan struct{}
	kill   *time.Timer

	access   sync.Mutex
	process  *os.Process
	stopping bool
}

func (h *Host) start(workingDir string, spec *Spec, environment []string) (*guard, error) {
	guard := &guard{
		host:   h,
		name:   spec.Name,
		dir:    workingDir,
		output: &lineWriter{logger: h.logger, prefix: "[" + spec.Name + "] "},
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	if err := guard.prepare(spec, environment); err != nil {
		guard.removeFiles()
		return nil, fmt.Errorf("%s: %w", spec.Name, err)
	}
	started := make(chan error, 1)
	go guard.run(started)
	if err := <-started; err != nil {
		<-guard.done
		guard.removeFiles()
		return nil, fmt.Errorf("start plugin %s: %w", spec.Name, err)
	}
	h.logger.Info("started plugin ", spec.Name)
	return guard, nil
}

func (g *guard) prepare(spec *Spec, environment []string) error {
	files := make(map[string]bool, len(spec.Files))
	for _, file := range spec.Files {
		path := filepath.Join(g.dir, file.Name)
		// Track before writing so a partial write is still removed.
		g.files = append(g.files, path)
		if err := os.WriteFile(path, []byte(file.Content), 0o600); err != nil {
			return err
		}
		files[file.Name] = true
	}
	g.cmd = make([]string, len(spec.Command))
	for index, argument := range spec.Command {
		expanded, err := expand(argument, g.dir, files)
		if err != nil {
			return err
		}
		g.cmd[index] = expanded
	}
	// exec keeps the last value of a duplicated key, so these override.
	g.env = append(make([]string, 0, len(environment)+len(spec.Environment)), environment...)
	for key, value := range spec.Environment {
		expanded, err := expand(value, g.dir, files)
		if err != nil {
			return err
		}
		g.env = append(g.env, key+"="+expanded)
	}
	return nil
}

func (g *guard) command() *exec.Cmd {
	cmd := exec.Command(g.cmd[0], g.cmd[1:]...)
	cmd.Dir = g.dir
	cmd.Env = g.env
	// One writer for both streams makes exec share a single pipe and copier.
	cmd.Stdout = g.output
	cmd.Stderr = g.output
	cmd.WaitDelay = pipeGrace
	// The core stops its plugins itself; its own process group signals must
	// not race that. PDEATHSIG covers a core that is killed outright.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	return cmd
}

func (g *guard) run(started chan<- error) {
	// Linux PDEATHSIG watches the forking thread, and a JNI caller's thread
	// may exit at any time. Fork every run from this pinned thread instead.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(g.done)
	delay := minRestartDelay
	for first := true; ; first = false {
		cmd := g.command()
		err := cmd.Start()
		if first {
			started <- err
		}
		if err != nil {
			if !first {
				g.fail(fmt.Errorf("restart %s: %w", g.name, err))
			}
			return
		}
		g.attach(cmd.Process)
		began := time.Now()
		err = cmd.Wait()
		lived := time.Since(began)
		g.detach(cmd.Process)
		g.output.flush()
		if g.isStopping() {
			return
		}
		if first && lived < fatalWindow {
			g.fail(fmt.Errorf("%s exited at startup: %v", g.name, err))
			return
		}
		if lived >= stableWindow {
			delay = minRestartDelay
		}
		g.host.logger.Warn("plugin ", g.name, " exited (", err, "), restarting in ", delay)
		timer := time.NewTimer(delay)
		select {
		case <-g.stop:
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, maxRestartDelay)
	}
}

func (g *guard) attach(process *os.Process) {
	g.access.Lock()
	g.process = process
	stopping := g.stopping
	g.access.Unlock()
	g.host.children.Store(process.Pid, struct{}{})
	if stopping {
		// The stop request raced this start and found no process to signal.
		_ = process.Kill()
	}
}

func (g *guard) detach(process *os.Process) {
	g.host.children.Delete(process.Pid)
	g.access.Lock()
	g.process = nil
	g.access.Unlock()
}

func (g *guard) isStopping() bool {
	g.access.Lock()
	defer g.access.Unlock()
	return g.stopping
}

func (g *guard) exited() bool {
	select {
	case <-g.done:
		return true
	default:
		return false
	}
}

func (g *guard) fail(err error) {
	if !g.isStopping() {
		g.host.fatal(err)
	}
}

func (g *guard) requestStop() {
	g.access.Lock()
	defer g.access.Unlock()
	if g.stopping {
		return
	}
	g.stopping = true
	close(g.stop)
	if g.process != nil {
		_ = g.process.Signal(syscall.SIGTERM)
		g.kill = time.AfterFunc(stopGrace, g.killGroup)
	}
}

// killGroup also reaches processes the plugin spawned into its group.
func (g *guard) killGroup() {
	g.access.Lock()
	defer g.access.Unlock()
	if g.process != nil {
		_ = syscall.Kill(-g.process.Pid, syscall.SIGKILL)
	}
}

func (g *guard) waitStopped() {
	timer := time.NewTimer(stopGrace + pipeGrace + time.Second)
	select {
	case <-g.done:
	case <-timer.C:
		g.host.logger.Error("plugin ", g.name, " did not stop")
	}
	timer.Stop()
	g.access.Lock()
	if g.kill != nil {
		g.kill.Stop()
	}
	g.access.Unlock()
	g.removeFiles()
}

func (g *guard) removeFiles() {
	for _, path := range g.files {
		_ = os.Remove(path)
	}
}

// stopGuards signals every plugin first so their grace periods overlap.
func stopGuards(guards []*guard) {
	for _, guard := range guards {
		guard.requestStop()
	}
	for _, guard := range guards {
		guard.waitStopped()
	}
}

// lineWriter turns plugin output into one log entry per line.
type lineWriter struct {
	logger Logger
	prefix string
	access sync.Mutex
	buffer []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.access.Lock()
	defer w.access.Unlock()
	written := len(p)
	for {
		index := bytes.IndexByte(p, '\n')
		if index < 0 {
			break
		}
		w.buffer = append(w.buffer, p[:index]...)
		w.emit()
		p = p[index+1:]
	}
	w.buffer = append(w.buffer, p...)
	if len(w.buffer) >= maxLine {
		w.emit()
	}
	return written, nil
}

func (w *lineWriter) flush() {
	w.access.Lock()
	defer w.access.Unlock()
	w.emit()
}

func (w *lineWriter) emit() {
	line := bytes.TrimSuffix(w.buffer, []byte{'\r'})
	if len(line) > maxLine {
		line = line[:maxLine]
	}
	if len(line) > 0 {
		w.logger.Info(w.prefix, string(line))
	}
	w.buffer = w.buffer[:0]
}
