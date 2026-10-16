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
//
// Options reach this package over the stock StartedService.URLTest gRPC
// method as protobuf unknown fields (see AppendURLTestOptions), so the
// generated protobuf and reF1nd/sing-box itself stay untouched. The
// prepare_sources.py patch bridges the stock handler to handleURLTestOptions;
// a stock unpatched daemon ignores the unknown fields and still answers the
// plain URLTest.

package daemon

import (
	"cmp"
	"context"
	"crypto/tls"
	"math"
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
	"github.com/sagernet/sing-snell"
	"github.com/sagernet/sing/common"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
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
// group selector does. A failed measurement drops the stale history entry,
// and every urltest group containing the measured outbound re-runs its
// selection against the updated history (clash API delay test behaviour).
// Cancelling ctx aborts the measurement.
//
// Port of husi's urltest.RunTag + Run (libcore/urltest/urltest.go:71-138).
func (s *StartedService) URLTestWithOptions(
	ctx context.Context,
	outboundTag string,
	link string,
	timeoutMs int32,
	flags URLTestFlags,
) (int32, error) {
	instance, detour, err := s.urlTestOptionsTarget(outboundTag)
	if err != nil {
		return -1, err
	}
	instanceCtx := instance.ctx
	if instanceCtx == nil {
		return -1, status.Error(codes.FailedPrecondition, "instance context is not available")
	}
	if boxurltest.UnifiedDelayFromContext(instanceCtx) {
		flags |= URLTestUnifiedDelay
	}
	latency, err := urlTestRunWithTimeout(ctx, instanceCtx, detour, link, timeoutMs, flags)

	historyStorage := instance.urlTestHistoryStorage
	if historyStorage != nil {
		realTag := group.RealTag(detour, N.NetworkTCP)
		if err != nil {
			historyStorage.DeleteURLTestHistory(realTag)
		} else {
			historyStorage.StoreURLTestHistory(realTag, &adapter.URLTestHistory{
				Time:  time.Now(),
				Delay: latency,
			})
		}
		urlTestUpdateGroups(instance.outboundManager, realTag)
	}
	if err != nil {
		return -1, err
	}
	return int32(latency), nil
}

func (s *StartedService) urlTestOptionsTarget(outboundTag string) (*Instance, adapter.Outbound, error) {
	s.serviceAccess.RLock()
	instance := s.instance
	s.serviceAccess.RUnlock()
	if instance == nil {
		return nil, nil, status.Error(codes.FailedPrecondition, "service is not started")
	}
	outboundManager := instance.outboundManager
	if outboundManager == nil {
		return nil, nil, status.Error(codes.FailedPrecondition, "outbound manager is not available")
	}
	if outboundTag == "" {
		return instance, outboundManager.Default(), nil
	}
	detour, loaded := outboundManager.Outbound(outboundTag)
	if !loaded {
		return nil, nil, status.Error(codes.NotFound, "outbound not found: "+outboundTag)
	}
	return instance, detour, nil
}

// urlTestUpdateGroups lets every urltest group that (transitively) contains
// realTag re-select against the fresh history. Mirrors the clash API delay
// endpoint (experimental/clashapi/proxies.go).
func urlTestUpdateGroups(outboundManager adapter.OutboundManager, realTag string) {
	if outboundManager == nil {
		return
	}
	for _, detour := range outboundManager.Outbounds() {
		urlTestGroup, isURLTestGroup := detour.(adapter.URLTestGroup)
		if !isURLTestGroup {
			continue
		}
		if !urlTestGroupContains(outboundManager, urlTestGroup, realTag, map[string]bool{detour.Tag(): true}) {
			continue
		}
		urlTestGroup.PerformUpdateCheck()
	}
}

func urlTestGroupContains(outboundManager adapter.OutboundManager, outboundGroup adapter.OutboundGroup, tag string, visited map[string]bool) bool {
	for _, member := range outboundGroup.All() {
		if member == tag {
			return true
		}
		if visited[member] {
			continue
		}
		detour, loaded := outboundManager.Outbound(member)
		if !loaded {
			continue
		}
		nestedGroup, isGroup := detour.(adapter.OutboundGroup)
		if !isGroup {
			continue
		}
		visited[member] = true
		if urlTestGroupContains(outboundManager, nestedGroup, tag, visited) {
			return true
		}
	}
	return false
}

// Wire format: the URL test options travel to the daemon as protobuf
// unknown fields on the stock StartedService.URLTest request, so neither
// the generated protobuf nor reF1nd/sing-box is modified. gRPC-go's
// default codec preserves unknown fields end to end (field 1 stays the
// outbound tag):
//
//	1000 (bytes)  — test link
//	1001 (varint) — timeout in milliseconds
//	1002 (varint) — URLTestFlags bitmask
//
// A stock, unpatched daemon ignores the unknown fields and performs the
// plain URLTest; a stock client cannot produce them.
const (
	urlTestOptionFieldLink    protowire.Number = 1000
	urlTestOptionFieldTimeout protowire.Number = 1001
	urlTestOptionFieldFlags   protowire.Number = 1002
)

// urlTestOptionsRequest is the decoded form of the unknown-field payload.
type urlTestOptionsRequest struct {
	Link      string
	TimeoutMs int32
	Flags     URLTestFlags
}

// AppendURLTestOptions attaches the options to request as unknown fields.
// The request must be freshly built by the caller: SetUnknown replaces any
// previously attached payload.
func AppendURLTestOptions(request *URLTestRequest, link string, timeoutMs int32, flags URLTestFlags) {
	var unknown []byte
	if link != "" {
		unknown = protowire.AppendTag(unknown, urlTestOptionFieldLink, protowire.BytesType)
		unknown = protowire.AppendString(unknown, link)
	}
	if timeoutMs > 0 {
		unknown = protowire.AppendTag(unknown, urlTestOptionFieldTimeout, protowire.VarintType)
		unknown = protowire.AppendVarint(unknown, uint64(timeoutMs))
	}
	unknown = protowire.AppendTag(unknown, urlTestOptionFieldFlags, protowire.VarintType)
	unknown = protowire.AppendVarint(unknown, uint64(flags))
	request.ProtoReflect().SetUnknown(unknown)
}

// urlTestOptionsFromRequest reports whether request carries the options
// envelope and returns its decoded form. A request without unknown fields,
// or with unknown fields outside this package's field numbers, is a stock
// URL test and returns loaded == false.
func urlTestOptionsFromRequest(request *URLTestRequest) (urlTestOptionsRequest, bool) {
	unknown := request.ProtoReflect().GetUnknown()
	var options urlTestOptionsRequest
	var loaded bool
	for len(unknown) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(unknown)
		if tagLength < 0 {
			return urlTestOptionsRequest{}, false
		}
		unknown = unknown[tagLength:]
		var valueLength int
		switch number {
		case urlTestOptionFieldLink:
			if wireType != protowire.BytesType {
				return urlTestOptionsRequest{}, false
			}
			options.Link, valueLength = protowire.ConsumeString(unknown)
			loaded = true
		case urlTestOptionFieldTimeout:
			if wireType != protowire.VarintType {
				return urlTestOptionsRequest{}, false
			}
			value, n := protowire.ConsumeVarint(unknown)
			if n < 0 {
				return urlTestOptionsRequest{}, false
			}
			options.TimeoutMs = int32(min(value, math.MaxInt32))
			loaded = true
			valueLength = n
		case urlTestOptionFieldFlags:
			if wireType != protowire.VarintType {
				return urlTestOptionsRequest{}, false
			}
			value, n := protowire.ConsumeVarint(unknown)
			if n < 0 {
				return urlTestOptionsRequest{}, false
			}
			options.Flags = URLTestFlags(uint8(value))
			loaded = true
			valueLength = n
		default:
			valueLength = protowire.ConsumeFieldValue(number, wireType, unknown)
		}
		if valueLength < 0 {
			return urlTestOptionsRequest{}, false
		}
		unknown = unknown[valueLength:]
	}
	return options, loaded
}

// handleURLTestOptions is the hook the prepare_sources.py patch calls from
// the stock StartedService.URLTest handler. It reports handled == false for
// a stock request, and for a urltest group target: such a group owns its
// link and selection, so it stays on the stock CheckOutbounds path.
//
// The measurement runs synchronously so a blocked gRPC handler throttles
// real measurements — that is what makes the caller's worker pool
// concurrency limit meaningful (husi awaits the same way).
func (s *StartedService) handleURLTestOptions(ctx context.Context, request *URLTestRequest) (bool, error) {
	options, loaded := urlTestOptionsFromRequest(request)
	if !loaded {
		return false, nil
	}
	_, detour, err := s.urlTestOptionsTarget(request.OutboundTag)
	if err != nil {
		return true, err
	}
	if _, isURLTestGroup := detour.(*group.URLTest); isURLTestGroup {
		return false, nil
	}
	_, err = s.URLTestWithOptions(ctx, request.OutboundTag, options.Link, options.TimeoutMs, options.Flags)
	if err != nil {
		if _, isStatus := status.FromError(err); isStatus {
			return true, err
		}
		return true, status.Error(codes.Internal, err.Error())
	}
	return true, nil
}

// urlTestRunWithTimeout bounds the measurement by timeoutMs and aborts it
// when requestCtx is cancelled (husi urltest.go:106-137, minus the goroutine
// indirection: gomobile callers are already off the UI thread). The
// measurement context derives from instanceCtx, which carries the NTP time
// and root CA pool the TLS config reads.
func urlTestRunWithTimeout(
	requestCtx context.Context,
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
	if requestCtx != nil {
		stop := context.AfterFunc(requestCtx, cancel)
		defer stop()
	}
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
// reF1nd/sing-box); the QUIC marker lives behind the with_quic build tag.
func urlTestContextWithKeepSession(ctx context.Context) context.Context {
	ctx = adapter.ContextWithKeepSession(ctx)
	ctx = mux.ContextWithKeepSession(ctx)
	ctx = anytls.ContextWithKeepSession(ctx)
	ctx = urlTestContextWithQUICKeepSession(ctx)
	ctx = snell.ContextWithKeepSession(ctx)
	return ctx
}

// urlTestMeasureOnce is the actual HEAD-request measurement. Port of husi
// urltest.go:173-253. With unified delay, a failed second request falls back
// to the first request's latency (reF1nd common/urltest behaviour).
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
	// No client-level Timeout: the request carries the caller's per-test
	// deadline through http.NewRequestWithContext (urlTestRunWithTimeout),
	// so a caller-supplied timeoutMs larger than C.TCPTimeout is honoured
	// instead of being cut short by a fixed client timeout.
	client := http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		return
	}
	response.Body.Close()
	elapsed := time.Since(start)
	if flags&URLTestUnifiedDelay != 0 {
		second := time.Now()
		secondResponse, secondErr := client.Do(req)
		if secondErr == nil {
			secondResponse.Body.Close()
			elapsed = time.Since(second)
		}
	}
	t = uint16(min(elapsed/time.Millisecond, math.MaxUint16))
	return
}
