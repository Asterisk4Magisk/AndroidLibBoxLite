// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0

package dns

import (
	"context"

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
func BeginClientDNSEvent(ctx context.Context, router adapter.DNSRouter, request *mDNS.Msg, metadata *adapter.InboundContext) (context.Context, *dnsevents.Observation) {
	if !dnsevents.ObservationEnabled() {
		return ctx, nil
	}
	dnsRouter, ok := router.(*Router)
	if !ok {
		return ctx, nil
	}
	feed := dnsRouter.DNSEventFeed()
	if !feed.Enabled() {
		return ctx, nil
	}
	var uid *uint32
	if owner := metadata.ProcessInfo; dnsRouter.dnsEventFindProcess && owner != nil && owner.UserId >= 0 {
		value := uint32(owner.UserId)
		// A ROOT redirect can identify netd's socket as UID 0. It cannot
		// tell a relayed resolver query from DNS sent directly by root.
		if value != 0 || !environment.IsCLI || !redirectedDNS(ctx, metadata) {
			uid = &value
		}
	}
	return feed.Start(ctx, request, uid)
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
