// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0
//
// URL test with options — full parity with husi's custom URL test.
//
// Injected by Asterisk4Magisk/AndroidLibBoxLite. Adapted from husi
// libcore/urltest/urltest.go (GPL-3.0, https://github.com/xchacha20-poly1305/husi).
// Rewritten to run inside the reF1nd/sing-box daemon package so the custom
// measurement can reach the instance's private outboundManager and
// urlTestHistoryStorage.

package daemon

import (
	"cmp"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/sagernet/sing-anytls"
	"github.com/sagernet/sing-box/adapter"
	boxurltest "github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/protocol/group"
	"github.com/sagernet/sing-mux"
	qtls "github.com/sagernet/sing-quic"
	"github.com/sagernet/sing-snell"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
)

// URLTestFlags is the option bitmask for URLTestWithOptions. Mirrors husi's
// urltest.Flags (libcore/urltest/urltest.go:37-42).
type URLTestFlags uint8

const (
	// URLTestUnifiedDelay counts the latency from the request rather than
	// from the connection, which hides the cost of a handshake the proxy
	// would have paid anyway.
	URLTestUnifiedDelay URLTestFlags = 1 << iota
	// URLTestIgnoreHandshakeTime reports the response time alone, leaving
	// the handshake out entirely.
	URLTestIgnoreHandshakeTime
)

// UrlTestDefaultLink mirrors husi urltest.go:164.
const UrlTestDefaultLink = "https://www.gstatic.com/generate_204"

const (
	urlTestSchemeHTTP  = "http"
	urlTestSchemeHTTPS = "https"
	urlTestSchemeHTTP3 = "http3"
	urlTestSchemeQUIC  = "quic"
)

// URLTestWithOptions measures outboundTag (or the default outbound when the
// tag is empty) through the running instance and stores the result in the
// instance's URL test history, so the group UI sees the same number the
// group selector does.
//
// Port of husi's urltest.RunTag + Run (libcore/urltest/urltest.go:71-138).
func (s *StartedService) URLTestWithOptions(
	outboundTag string,
	link string,
	timeoutMs int32,
	flags URLTestFlags,
) (int32, error) {
	s.serviceAccess.RLock()
	instance := s.instance
	s.serviceAccess.RUnlock()
	if instance == nil {
		return -1, E.New("service is not started")
	}
	outboundManager := instance.outboundManager
	if outboundManager == nil {
		return -1, E.New("outbound manager is not available")
	}
	var detour adapter.Outbound
	if outboundTag == "" {
		detour = outboundManager.Default()
	} else {
		var loaded bool
		detour, loaded = outboundManager.Outbound(outboundTag)
		if !loaded {
			return -1, E.New("outbound not found: ", outboundTag)
		}
	}
	instanceCtx := instance.ctx
	if instanceCtx == nil {
		return -1, E.New("instance context is not available")
	}
	latency, err := urlTestRunWithTimeout(instanceCtx, detour, link, timeoutMs, flags)
	if err != nil {
		return -1, err
	}

	// Store the result so the UI sees the same number the group selector
	// does (husi urltest.go:120-127).
	historyStorage := instance.urlTestHistoryStorage
	if historyStorage != nil {
		historyStorage.StoreURLTestHistory(group.RealTag(detour, N.NetworkTCP), &adapter.URLTestHistory{
			Time:  time.Now(),
			Delay: latency,
		})
	}
	return int32(latency), nil
}

// urlTestRunWithTimeout bounds the measurement by timeoutMs (husi
// urltest.go:106-137, minus the goroutine indirection: gomobile callers are
// already off the UI thread).
func urlTestRunWithTimeout(
	instanceCtx context.Context,
	detour adapter.Outbound,
	link string,
	timeoutMs int32,
	flags URLTestFlags,
) (uint16, error) {
	if timeoutMs <= 0 {
		timeoutMs = int32(C.TCPTimeout / time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(instanceCtx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	return urlTestMeasure(ctx, link, detour, flags)
}

// urlTestMeasure times a HEAD request through detour. A multiplexed outbound
// is warmed up with a preceding request that keeps its session alive, so the
// measured request rides the established session instead of paying for the
// handshake. Port of husi urltest.go:143-152.
func urlTestMeasure(ctx context.Context, link string, detour N.Dialer, flags URLTestFlags) (uint16, error) {
	multiplexOutbound, isMultiplexOutbound := common.Cast[adapter.OutboundWithMultiplex](detour)
	if isMultiplexOutbound && multiplexOutbound.MultiplexEnabled() {
		_, err := urlTestMeasureOnce(urlTestContextWithKeepSession(ctx), link, detour, flags)
		if err != nil {
			return 0, err
		}
	}
	return urlTestMeasureOnce(ctx, link, detour, flags)
}

// urlTestContextWithKeepSession marks the context so multiplex-capable
// protocols keep the session across the warmup and measured requests.
// Mirrors husi urltest.go:154-162 minus trusttunnel (not present in
// reF1nd/sing-box).
func urlTestContextWithKeepSession(ctx context.Context) context.Context {
	ctx = adapter.ContextWithKeepSession(ctx)
	ctx = mux.ContextWithKeepSession(ctx)
	ctx = anytls.ContextWithKeepSession(ctx)
	ctx = qtls.ContextWithKeepSession(ctx)
	ctx = snell.ContextWithKeepSession(ctx)
	return ctx
}

// urlTestMeasureOnce is the actual HEAD-request measurement. Port of husi
// urltest.go:173-253.
func urlTestMeasureOnce(ctx context.Context, link string, detour N.Dialer, flags URLTestFlags) (t uint16, err error) {
	link = cmp.Or(link, UrlTestDefaultLink)
	linkURL, err := url.Parse(link)
	if err != nil {
		return
	}
	network := N.NetworkTCP
	isHTTP3 := linkURL.Scheme == urlTestSchemeHTTP3 || linkURL.Scheme == urlTestSchemeQUIC
	if isHTTP3 {
		if !C.WithQUIC {
			return 0, C.ErrQUICNotIncluded
		}
		network = N.NetworkUDP
		linkURL.Scheme = urlTestSchemeHTTPS
	}
	hostname := linkURL.Hostname()
	port := linkURL.Port()
	if port == "" {
		switch linkURL.Scheme {
		case urlTestSchemeHTTP:
			port = "80"
		case urlTestSchemeHTTPS:
			port = "443"
		}
	}

	start := time.Now()
	instance, err := detour.DialContext(ctx, network, M.ParseSocksaddrHostPortStr(hostname, port))
	if err != nil {
		return
	}
	defer instance.Close()
	if flags&URLTestIgnoreHandshakeTime != 0 && N.NeedHandshakeForWrite(instance) {
		start = time.Now()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, linkURL.String(), nil)
	if err != nil {
		return
	}
	tlsConfig := &tls.Config{
		Time:    ntp.TimeFuncFromContext(ctx),
		RootCAs: adapter.RootPoolFromContext(ctx),
	}
	var transport http.RoundTripper
	if isHTTP3 {
		transport, err = urlTestNewHTTP3Transport(instance, tlsConfig)
		if err != nil {
			return
		}
		defer common.Close(transport)
	} else {
		transport = &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return instance, nil
			},
			TLSClientConfig: tlsConfig,
		}
	}
	client := http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: C.TCPTimeout,
	}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		return
	}
	defer response.Body.Close()
	if flags&URLTestUnifiedDelay != 0 {
		start = time.Now()
		response, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
	}
	t = uint16(time.Since(start) / time.Millisecond)
	return
}

// Compile-time assertion that the history storage type matches upstream —
// guards against reF1nd changing the common/urltest package layout.
var _ = boxurltest.NewHistoryStorage
