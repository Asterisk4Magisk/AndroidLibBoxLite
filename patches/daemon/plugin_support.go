// Copyright (C) 2026 AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0
//
// Plugin process support: protocol plugins (Mieru, ShadowQUIC, ...) run as
// child processes of the core, supervised by this pool.
//
// Adapted from xchacha20-poly1305/husi libcore/pluginpool (GPL-3.0).

package daemon

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	// pluginFatalExitWindow: a plugin that exits within this window is a
	// configuration error, not a crash loop; the service must stop.
	pluginFatalExitWindow = time.Second
	pluginStopGracePeriod = time.Second
	// pluginMaxLineLength bounds one buffered log line before it is flushed raw.
	pluginMaxLineLength = 1024 * 1024

	pluginFileTokenPrefix = "${file:"
	pluginFileTokenSuffix = "}"
)

// PluginFileSpec is one file written into the plugin working directory before
// the process starts, and removed when it stops.
type PluginFileSpec struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// PluginProcessSpec describes one plugin child process.
type PluginProcessSpec struct {
	Name        string            `json:"name"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment"`
	Files       []PluginFileSpec  `json:"files"`
}

// PluginOptions is the JSON envelope the app sends alongside a start request.
type PluginOptions struct {
	WorkingDir  string              `json:"workingDir"`
	ProtectPath string              `json:"protectPath"`
	VerifyPeer  bool                `json:"verifyPeer"`
	Plugins     []PluginProcessSpec `json:"plugins"`
}

// ParsePluginOptions decodes the app-side envelope. Empty content means the
// start request carries no plugins.
func ParsePluginOptions(content string) (*PluginOptions, error) {
	if strings.TrimSpace(content) == "" {
		return nil, nil
	}
	var options PluginOptions
	err := json.Unmarshal([]byte(content), &options)
	if err != nil {
		return nil, E.Cause(err, "decode plugin options")
	}
	if len(options.Plugins) == 0 {
		return nil, nil
	}
	if options.WorkingDir == "" {
		return nil, E.New("missing plugin working directory")
	}
	return &options, nil
}

// PluginProtector exempts one plugin socket from the tunnel.
type PluginProtector func(fd int) error

// PluginPool supervises the plugin child processes of one service instance.
type PluginPool struct {
	access     sync.Mutex
	workingDir string
	onFatal    func(error)
	protect    *pluginProtectServer
	guards     []*pluginGuard
	closed     bool
}

// StartPluginPool starts the protect server (when a path is set) and every
// plugin in options. The caller owns the returned pool and must Close it.
func StartPluginPool(options PluginOptions, protector PluginProtector, onFatal func(error)) (*PluginPool, error) {
	pool := &PluginPool{
		workingDir: options.WorkingDir,
		onFatal:    onFatal,
	}
	err := os.MkdirAll(options.WorkingDir, 0o700)
	if err != nil {
		return nil, E.Cause(err, "create plugin working directory")
	}
	if options.ProtectPath != "" {
		if protector == nil {
			return nil, E.New("protect path requires a protector")
		}
		server, serverErr := startPluginProtectServer(options.ProtectPath, options.VerifyPeer, protector)
		if serverErr != nil {
			return nil, E.Cause(serverErr, "start plugin protect server")
		}
		pool.protect = server
	}
	for index := range options.Plugins {
		if startErr := pool.start(&options.Plugins[index]); startErr != nil {
			_ = pool.Close()
			return nil, startErr
		}
	}
	return pool, nil
}

func (p *PluginPool) start(spec *PluginProcessSpec) error {
	p.access.Lock()
	defer p.access.Unlock()
	if p.closed {
		return E.New("plugin pool closed")
	}
	guard, err := newPluginGuard(p.workingDir, spec, p.handleFatal)
	if err != nil {
		return err
	}
	if err = guard.start(); err != nil {
		_ = guard.cleanupFiles()
		return err
	}
	p.guards = append(p.guards, guard)
	go guard.loop()
	return nil
}

// Close stops every plugin and the protect server. Safe to call twice.
func (p *PluginPool) Close() error {
	p.access.Lock()
	if p.closed {
		p.access.Unlock()
		return nil
	}
	p.closed = true
	guards := p.guards
	p.guards = nil
	protect := p.protect
	p.protect = nil
	p.access.Unlock()

	var errs []error
	for _, guard := range guards {
		if err := guard.stop(); err != nil {
			errs = append(errs, err)
		}
	}
	if protect != nil {
		if err := protect.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return E.Errors(errs...)
}

func (p *PluginPool) handleFatal(err error) {
	if err == nil || p.onFatal == nil {
		return
	}
	log.Error("plugin pool fatal: ", err)
	// Async so a fatal during start cannot deadlock on the caller's locks.
	go p.onFatal(err)
}

type pluginGuard struct {
	name       string
	command    []string
	env        []string
	workingDir string
	filePaths  []string
	onFatal    func(error)

	access  sync.Mutex
	cmd     *exec.Cmd
	process *os.Process
	stopCh  chan struct{}
	doneCh  chan struct{}
	stopped bool
}

func newPluginGuard(workingDir string, spec *PluginProcessSpec, onFatal func(error)) (*pluginGuard, error) {
	name := spec.Name
	if name == "" {
		name = "plugin"
	}
	if len(spec.Command) == 0 {
		return nil, E.New(name, ": empty command")
	}
	filePaths, fileAbs, err := writePluginFiles(workingDir, name, spec.Files)
	if err != nil {
		return nil, err
	}
	expandedCommand := make([]string, len(spec.Command))
	for index, argument := range spec.Command {
		expandedCommand[index] = expandPluginFileTokens(argument, fileAbs)
	}
	env := os.Environ()
	for key, value := range spec.Environment {
		env = append(env, key+"="+expandPluginFileTokens(value, fileAbs))
	}
	return &pluginGuard{
		name:       name,
		command:    expandedCommand,
		env:        env,
		workingDir: workingDir,
		filePaths:  filePaths,
		onFatal:    onFatal,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}, nil
}

func writePluginFiles(workingDir, pluginName string, files []PluginFileSpec) ([]string, map[string]string, error) {
	fileAbs := make(map[string]string, len(files))
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if validatePluginFileName(file.Name) != nil {
			cleanupPluginFiles(paths)
			return nil, nil, E.Cause(validatePluginFileName(file.Name), pluginName)
		}
		path := filepath.Join(workingDir, file.Name)
		if err := os.WriteFile(path, []byte(file.Content), 0o600); err != nil {
			cleanupPluginFiles(paths)
			return nil, nil, E.Cause(err, "write plugin file ", file.Name)
		}
		paths = append(paths, path)
		fileAbs[file.Name] = path
	}
	return paths, fileAbs, nil
}

func cleanupPluginFiles(paths []string) {
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

func validatePluginFileName(name string) error {
	if name == "" {
		return E.New("empty plugin file name")
	}
	if name != filepath.Base(name) || strings.Contains(name, `\`) || strings.Contains(name, "/") {
		return E.New("plugin file name must not contain a path separator: ", name)
	}
	if name == "." || name == ".." {
		return E.New("invalid plugin file name: ", name)
	}
	return nil
}

func expandPluginFileTokens(value string, files map[string]string) string {
	if !strings.Contains(value, pluginFileTokenPrefix) {
		return value
	}
	var builder strings.Builder
	remaining := value
	for {
		start := strings.Index(remaining, pluginFileTokenPrefix)
		if start < 0 {
			builder.WriteString(remaining)
			break
		}
		builder.WriteString(remaining[:start])
		remaining = remaining[start+len(pluginFileTokenPrefix):]
		end := strings.Index(remaining, pluginFileTokenSuffix)
		if end < 0 {
			builder.WriteString(pluginFileTokenPrefix)
			builder.WriteString(remaining)
			break
		}
		name := remaining[:end]
		remaining = remaining[end+len(pluginFileTokenSuffix):]
		if path, loaded := files[name]; loaded {
			builder.WriteString(path)
		} else {
			// Leave unknown tokens untouched so a misconfiguration stays visible.
			builder.WriteString(pluginFileTokenPrefix)
			builder.WriteString(name)
			builder.WriteString(pluginFileTokenSuffix)
		}
	}
	return builder.String()
}

var errPluginStopped = E.New("plugin process stopped")

func (g *pluginGuard) start() error {
	// Hold the lock across Start so a concurrent stop either sees the new
	// process or prevents it from starting at all.
	g.access.Lock()
	defer g.access.Unlock()
	if g.stopped {
		return errPluginStopped
	}
	cmd := exec.Command(g.command[0], g.command[1:]...)
	cmd.Dir = g.workingDir
	cmd.Env = g.env
	// Tie the plugin to this process: a core that is SIGKILLed (supervisor stop,
	// crash) must not leave plugin processes behind. The kernel keys this to the
	// forking thread, so it is best-effort; pool.Close is the primary stop path.
	if attribute := pluginDeathSignal(); attribute != nil {
		cmd.SysProcAttr = attribute
	}
	// Let os/exec own the pipes: Wait then drains them before returning, so the
	// last lines a plugin prints before exiting are never dropped.
	writer := &pluginLogWriter{name: g.name}
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err := cmd.Start(); err != nil {
		return E.Cause(err, "start process ", g.name)
	}
	g.cmd = cmd
	g.process = cmd.Process
	log.Info("start plugin process: ", strings.Join(g.command, " "))
	return nil
}

// pluginLogWriter turns arbitrary byte chunks into per-line core log entries.
// It never blocks: an over-long line is flushed raw instead of stalling the
// plugin on a full pipe.
type pluginLogWriter struct {
	name   string
	access sync.Mutex
	buffer []byte
}

func (w *pluginLogWriter) Write(p []byte) (int, error) {
	w.access.Lock()
	defer w.access.Unlock()
	w.buffer = append(w.buffer, p...)
	for {
		index := bytes.IndexByte(w.buffer, '\n')
		if index < 0 {
			break
		}
		line := strings.TrimSuffix(string(w.buffer[:index]), "\r")
		log.Debug("[", w.name, "] ", line)
		w.buffer = w.buffer[index+1:]
	}
	if len(w.buffer) > pluginMaxLineLength {
		log.Debug("[", w.name, "] ", string(w.buffer))
		w.buffer = nil
	}
	return len(p), nil
}

func (g *pluginGuard) loop() {
	defer close(g.doneCh)
	for {
		startTime := time.Now()
		exitErr := g.wait()
		select {
		case <-g.stopCh:
			return
		default:
		}
		exitCode := pluginExitCode(exitErr)
		if time.Since(startTime) < pluginFatalExitWindow {
			g.onFatal(E.New(g.name, " exits too fast (exit code: ", exitCode, ")"))
			return
		}
		log.Warn(g.name, " unexpectedly exits with code ", exitCode)
		log.Info("restart plugin process: ", strings.Join(g.command, " "))
		select {
		case <-g.stopCh:
			return
		default:
		}
		if err := g.start(); err != nil {
			if err == errPluginStopped {
				return
			}
			g.onFatal(E.Cause(err, "restart process ", g.name))
			return
		}
	}
}

func (g *pluginGuard) wait() error {
	g.access.Lock()
	cmd := g.cmd
	g.access.Unlock()
	if cmd == nil {
		return E.New("process not started")
	}
	return cmd.Wait()
}

func (g *pluginGuard) stop() error {
	g.access.Lock()
	if g.stopped {
		g.access.Unlock()
		return nil
	}
	g.stopped = true
	close(g.stopCh)
	process := g.process
	g.access.Unlock()

	if process != nil {
		_ = process.Signal(syscall.SIGTERM)
		select {
		case <-g.doneCh:
		case <-time.After(pluginStopGracePeriod):
			_ = process.Kill()
			select {
			case <-g.doneCh:
			case <-time.After(pluginStopGracePeriod):
			}
		}
	} else {
		<-g.doneCh
	}
	return g.cleanupFiles()
}

func (g *pluginGuard) cleanupFiles() error {
	var errs []error
	for _, path := range g.filePaths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return E.Errors(errs...)
}

func pluginExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, isExit := err.(*exec.ExitError); isExit {
		return exitErr.ExitCode()
	}
	return -1
}

// StartPluginsFromConfig starts the CLI-mode plugin pool from the envelope the
// app publishes next to its config file. The CLI's working directory is not
// reliable (asteriskd runs the core with cwd "/"), so the config path decides.
//
// A malformed envelope is an error: running without the plugins an outbound
// needs would degrade the service silently.
func StartPluginsFromConfig(configPaths []string) (func(), error) {
	for _, configPath := range configPaths {
		if configPath == "" {
			continue
		}
		directory := filepath.Dir(configPath)
		if directory == "" || directory == "." {
			continue
		}
		if _, err := os.Stat(filepath.Join(directory, "plugin.json")); err == nil {
			return StartPluginsFromDir(directory)
		}
	}
	return StartPluginsFromDir(".")
}

// StartPluginsFromDir starts the CLI-mode plugin pool from <dir>/plugin.json,
// the sidecar BOX publishes next to the core config in ROOT mode. A missing
// sidecar means the start carries no plugins; the returned stop function is
// safe to defer.
func StartPluginsFromDir(directory string) (func(), error) {
	content, err := os.ReadFile(filepath.Join(directory, "plugin.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return func() {}, nil
		}
		return nil, E.Cause(err, "read plugin envelope")
	}
	options, err := ParsePluginOptions(string(content))
	if err != nil {
		return nil, E.Cause(err, "parse plugin envelope")
	}
	if options == nil {
		return func() {}, nil
	}
	pool, err := StartPluginPool(*options, nil, func(fatal error) {
		// The core cannot serve an outbound whose plugin is gone: exit and let
		// the supervisor report it instead of running degraded.
		log.Error("plugin process failed, exiting: ", fatal)
		go func() {
			time.Sleep(pluginStopGracePeriod)
			log.Fatal("plugin process failed: ", fatal)
		}()
	})
	if err != nil {
		return nil, E.Cause(err, "start plugin processes")
	}
	return func() {
		if closeErr := pool.Close(); closeErr != nil {
			log.Error("stop plugin processes: ", closeErr)
		}
	}, nil
}
