// Copyright (C) 2026 AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0
//
// Plugin-aware start entry point: the app compiles the plugin processes along
// with the config and sends both in one call.

package libbox

import (
	"sync"

	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
)

// pluginToken identifies one pool instance so a fatal callback from a pool that
// has already been replaced cannot stop the healthy one.
type pluginToken struct{}

// pluginServerState owns the plugin pool of one CommandServer. The pool is
// swapped on reload, so every access goes through mu.
type pluginServerState struct {
	mu    sync.Mutex
	pool  *daemon.PluginPool
	token *pluginToken
}

// pluginStateInit serializes lazy creation of the per-server state.
var pluginStateInit sync.Mutex

func (s *CommandServer) pluginStateOf() *pluginServerState {
	pluginStateInit.Lock()
	defer pluginStateInit.Unlock()
	if s.pluginState == nil {
		s.pluginState = &pluginServerState{}
	}
	return s.pluginState
}

// StartOrReloadServiceWithPlugins starts (or reloads) the service together with
// the plugin processes described by pluginOptionsJSON. An empty envelope
// behaves exactly like StartOrReloadService.
func (s *CommandServer) StartOrReloadServiceWithPlugins(configContent string, options *OverrideOptions, pluginOptionsJSON string) error {
	pluginOptions, err := daemon.ParsePluginOptions(pluginOptionsJSON)
	if err != nil {
		return E.Cause(err, "parse plugin options")
	}
	state := s.pluginStateOf()
	// Hold the lock across stop-old/start-new so a concurrent Close cannot see
	// "no pool" and leave the new one running after the service is gone.
	state.mu.Lock()
	oldPool := state.pool
	state.pool = nil
	state.token = nil
	if pluginOptions != nil {
		token := &pluginToken{}
		pool, startErr := daemon.StartPluginPool(
			*pluginOptions,
			s.protectPluginSocket,
			func(fatal error) { s.handlePluginFatal(token, fatal) },
		)
		if startErr != nil {
			state.mu.Unlock()
			s.closePluginPool(oldPool)
			return E.Cause(startErr, "start plugin processes")
		}
		state.pool = pool
		state.token = token
	}
	state.mu.Unlock()
	s.closePluginPool(oldPool)
	if err = s.StartOrReloadService(configContent, options); err != nil {
		s.stopPlugins()
		return err
	}
	return nil
}

// protectPluginSocket routes one plugin socket through the platform hook the
// core already uses for its own outbound sockets (VpnService.protect on BOX).
func (s *CommandServer) protectPluginSocket(fd int) error {
	if s.platformInterface == nil {
		return E.New("no platform interface for socket protection")
	}
	return s.platformInterface.AutoDetectInterfaceControl(int32(fd))
}

func (s *CommandServer) handlePluginFatal(token *pluginToken, fatal error) {
	log.Error("plugin process failed: ", fatal)
	state := s.pluginStateOf()
	state.mu.Lock()
	current := state.token
	state.mu.Unlock()
	if current != token {
		// A pool that has already been replaced: its death is history.
		return
	}
	if s.handler != nil {
		_ = s.handler.ServiceStop()
	}
}

func (s *CommandServer) stopPlugins() {
	state := s.pluginStateOf()
	state.mu.Lock()
	pool := state.pool
	state.pool = nil
	state.token = nil
	state.mu.Unlock()
	s.closePluginPool(pool)
}

func (s *CommandServer) closePluginPool(pool *daemon.PluginPool) {
	if pool == nil {
		return
	}
	if err := pool.Close(); err != nil {
		log.Error("stop plugin processes: ", err)
	}
}
