// Copyright (C) 2026 AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0
//
// Plugin-aware start entry point: the app compiles the plugin processes along
// with the config and sends both in one call.

package libbox

import (
	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
)

// StartOrReloadServiceWithPlugins starts (or reloads) the service together with
// the plugin processes described by pluginOptionsJSON. An empty envelope
// behaves exactly like StartOrReloadService.
func (s *CommandServer) StartOrReloadServiceWithPlugins(configContent string, options *OverrideOptions, pluginOptionsJSON string) error {
	pluginOptions, err := daemon.ParsePluginOptions(pluginOptionsJSON)
	if err != nil {
		return E.Cause(err, "parse plugin options")
	}
	// A reload tears the previous plugin processes down before the new
	// instance starts, so the ports they hold are released.
	s.stopPlugins()
	if pluginOptions != nil {
		pool, startErr := daemon.StartPluginPool(*pluginOptions, s.protectPluginSocket, s.pluginFatal)
		if startErr != nil {
			return E.Cause(startErr, "start plugin processes")
		}
		s.pluginPool = pool
	}
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

func (s *CommandServer) pluginFatal(err error) {
	log.Error("plugin process failed: ", err)
	if s.handler != nil {
		_ = s.handler.ServiceStop()
	}
}

func (s *CommandServer) stopPlugins() {
	pool := s.pluginPool
	if pool == nil {
		return
	}
	s.pluginPool = nil
	if err := pool.Close(); err != nil {
		log.Error("stop plugin processes: ", err)
	}
}
