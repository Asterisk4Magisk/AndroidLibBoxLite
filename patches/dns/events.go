// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0

package dns

import (
	"context"
	"net/netip"

	mDNS "github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	environment "github.com/sagernet/sing-box/common/androidcli"
	"github.com/sagernet/sing-box/common/androidcli/dnsevents"
	"github.com/sagernet/sing-box/common/androidcli/flowmeta"
	C "github.com/sagernet/sing-box/constant"
)

func (r *Router) DNSEventFeed() *dnsevents.Feed { return &r.dnsEvents }

// BeginClientDNSEvent is called by DNS wire handlers. Internal router and
// dialer lookups inherit context but do not start events of their own.
func BeginClientDNSEvent(ctx context.Context, router adapter.DNSRouter, request *mDNS.Msg, metadata *adapter.InboundContext, network string, destination netip.AddrPort, wire []byte) (context.Context, *dnsevents.Observation) {
	if !dnsevents.ObservationEnabled() {
		return ctx, nil
	}
	provider, ok := router.(dnsevents.FeedProvider)
	if !ok {
		return ctx, nil
	}
	feed := provider.DNSEventFeed()
	if !feed.Enabled() {
		return ctx, nil
	}
	var uid *uint32
	var uidSource string
	if owner := metadata.ProcessInfo; owner != nil && owner.UserId >= 0 {
		value := uint32(owner.UserId)
		// A ROOT redirect can identify netd's socket as UID 0. It cannot
		// tell a relayed resolver query from DNS sent directly by root.
		if value != 0 || !environment.IsCLI || !redirectedDNS(ctx, metadata) {
			uid, uidSource = &value, "socket"
		}
	}
	if environment.IsCLI {
		if enhanced, found := dnsevents.EnhancedUID(network, metadata.Source.AddrPort(), destination, wire); found {
			uid, uidSource = nil, ""
			if enhanced != dnsevents.UnknownEnhancedUID {
				uid, uidSource = &enhanced, "enhance"
			}
		}
	}
	return feed.Start(ctx, request, uid, uidSource)
}

func redirectedDNS(ctx context.Context, metadata *adapter.InboundContext) bool {
	switch metadata.InboundType {
	case C.TypeTun, C.TypeTProxy, C.TypeRedirect:
		return true
	default:
		_, bridged := flowmeta.HeaderFromContext(ctx)
		return bridged
	}
}

func DNSEventOrigin(transport adapter.DNSTransport) dnsevents.Origin {
	switch transport.Type() {
	case C.DNSTypeFakeIP:
		return dnsevents.Origin{Source: "fake-ip"}
	case C.DNSTypeHosts:
		return dnsevents.Origin{Source: "hosts"}
	}
	origin := dnsevents.Origin{Source: "upstream"}
	var ok bool
	origin.Peer, ok = transport.(interface{ DNSEventAddress() string })
	if !ok {
		return origin
	}
	switch kind := transport.Type(); kind {
	case C.DNSTypeUDP, C.DNSTypeTCP, C.DNSTypeTLS, C.DNSTypeHTTPS, C.DNSTypeQUIC:
		origin.Scheme = kind
	case C.DNSTypeHTTP3:
		origin.Scheme = "https"
	}
	if described, ok := transport.(interface{ DNSEventDirect() bool }); ok && described.DNSEventDirect() {
		origin.Route = "direct"
	}
	return origin
}
