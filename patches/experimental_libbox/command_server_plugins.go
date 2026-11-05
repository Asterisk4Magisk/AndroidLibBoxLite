// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0
//
// gomobile-facing plugin support: StartOrReloadServiceWithPlugins starts the
// service together with the plugin processes described by an envelope (see
// common/androidcli/pluginpool).
//
// Injected by Asterisk4Magisk/AndroidLibBoxLite. Hosts are kept beside the
// CommandServer instead of in a new field, so the only upstream changes are
// the closePlugins calls in Close and CloseService.

package libbox

import (
	"sync"

	"github.com/sagernet/sing-box/common/androidcli/pluginpool"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
)

var (
	pluginHostAccess sync.Mutex
	pluginHosts      = make(map[*CommandServer]*pluginpool.Host)
)

// StartOrReloadServiceWithPlugins applies the envelope before starting or
// reloading the service: plugins whose spec is unchanged keep running, and an
// empty envelope behaves exactly like StartOrReloadService.
func (s *CommandServer) StartOrReloadServiceWithPlugins(configContent string, options *OverrideOptions, pluginOptionsJSON string) error {
	pluginOptions, err := pluginpool.Parse(pluginOptionsJSON)
	if err != nil {
		return err
	}
	if pluginOptions == nil {
		s.closePlugins()
	} else if err = s.pluginHost().Apply(pluginOptions); err != nil {
		s.closePlugins()
		return E.Cause(err, "start plugins")
	}
	if err = s.StartOrReloadService(configContent, options); err != nil {
		s.closePlugins()
		return err
	}
	return nil
}

func (s *CommandServer) pluginHost() *pluginpool.Host {
	pluginHostAccess.Lock()
	defer pluginHostAccess.Unlock()
	host := pluginHosts[s]
	if host == nil {
		host = pluginpool.NewHost(pluginLogger{}, s.protectPluginSocket, func(err error) {
			s.pluginFailed(host, err)
		})
		pluginHosts[s] = host
	}
	return host
}

func (s *CommandServer) closePlugins() {
	pluginHostAccess.Lock()
	host := pluginHosts[s]
	delete(pluginHosts, s)
	pluginHostAccess.Unlock()
	if host != nil {
		_ = host.Close()
	}
}

// pluginFailed stops the service: an outbound whose plugin is gone cannot
// connect. A host that was already replaced or closed is ignored.
func (s *CommandServer) pluginFailed(host *pluginpool.Host, err error) {
	pluginHostAccess.Lock()
	current := pluginHosts[s] == host
	pluginHostAccess.Unlock()
	if current && s.handler != nil {
		_ = s.handler.ServiceStop()
	}
}

// protectPluginSocket uses the hook the core uses for its own sockets
// (VpnService.protect on Android).
func (s *CommandServer) protectPluginSocket(fd int) error {
	if s.platformInterface == nil {
		return E.New("missing platform interface")
	}
	return s.platformInterface.AutoDetectInterfaceControl(int32(fd))
}

type pluginLogger struct{}

func (pluginLogger) Info(args ...any)  { log.Info(args...) }
func (pluginLogger) Warn(args ...any)  { log.Warn(args...) }
func (pluginLogger) Error(args ...any) { log.Error(args...) }
