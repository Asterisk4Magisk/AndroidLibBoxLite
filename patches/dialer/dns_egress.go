// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: LGPL-3.0

package dialer

import (
	"context"
	"net"
	"reflect"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/androidcli/dnsevents"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type dnsEgressKey struct{}

type dnsDetourDialer struct {
	*DetourDialer
}

func (d *dnsDetourDialer) Upstream() any { return d.DetourDialer }

func (d *dnsDetourDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	dialer, err := d.Dialer()
	if err != nil {
		return nil, err
	}
	return DialOutboundWithDNSEgress(ctx, dialer.(adapter.Outbound), network, destination, dnsEgressFromContext(ctx))
}

// TracesDNSEgress reports whether this dialer can record an outbound.
func TracesDNSEgress(dialer N.Dialer) bool {
	_, ok := common.Cast[*dnsDetourDialer](dialer)
	return ok
}

// WithoutDNSEgress prevents a nested DNS attempt from recording an outer dial's egress.
func WithoutDNSEgress(ctx context.Context) context.Context {
	if dnsEgressFromContext(ctx) != nil {
		return context.WithValue(ctx, dnsEgressKey{}, (*DNSEgressTrace)(nil))
	}
	return ctx
}

// A parallel dial may complete more than one connection. Only facts for
// the returned connection can describe the query that used it.
type DNSEgressTrace struct {
	mu     sync.Mutex
	first  net.Conn
	egress dnsevents.Egress
	others map[net.Conn]dnsevents.Egress
}

func TrackDNSEgress(ctx context.Context, detour bool) (context.Context, *DNSEgressTrace) {
	if !detour {
		return ctx, nil
	}
	trace := new(DNSEgressTrace)
	return context.WithValue(ctx, dnsEgressKey{}, trace), trace
}

func dnsEgressFromContext(ctx context.Context) *DNSEgressTrace {
	trace, _ := ctx.Value(dnsEgressKey{}).(*DNSEgressTrace)
	return trace
}

func DialOutboundWithDNSEgress(ctx context.Context, outbound adapter.Outbound, network string, destination M.Socksaddr, trace *DNSEgressTrace) (net.Conn, error) {
	if trace == nil {
		return outbound.DialContext(ctx, network, destination)
	}
	if group, ok := outbound.(interface {
		DialContextWithDNSEgress(context.Context, string, M.Socksaddr, *DNSEgressTrace) (net.Conn, error)
	}); ok {
		return group.DialContextWithDNSEgress(ctx, network, destination, trace)
	}
	conn, err := outbound.DialContext(ctx, network, destination)
	if err == nil {
		if _, group := outbound.(adapter.OutboundGroup); !group {
			trace.recordOutbound(conn, outbound.Type(), outbound.Tag())
		}
	}
	return conn, err
}

func (t *DNSEgressTrace) recordOutbound(conn net.Conn, kind, tag string) {
	var egress dnsevents.Egress
	switch kind {
	case "", C.TypeBlock, C.TypeDNS:
		return
	case C.TypeDirect:
		egress = dnsevents.Egress{Route: "direct", Tag: tag}
	default:
		egress = dnsevents.Egress{Route: "proxy", Tag: tag}
	}
	t.record(conn, egress)
}

func (t *DNSEgressTrace) record(conn net.Conn, egress dnsevents.Egress) {
	if conn == nil || !reflect.ValueOf(conn).Comparable() || !t.mu.TryLock() {
		return
	}
	defer t.mu.Unlock()
	if t.first == nil || t.first == conn {
		t.first, t.egress = conn, egress
		return
	}
	if t.others == nil {
		t.others = make(map[net.Conn]dnsevents.Egress)
	}
	t.others[conn] = egress
}

func (t *DNSEgressTrace) Egress(conn net.Conn) dnsevents.Egress {
	if t == nil || !t.mu.TryLock() {
		return dnsevents.Egress{}
	}
	defer t.mu.Unlock()
	for conn != nil && reflect.ValueOf(conn).Comparable() {
		if t.first == conn {
			return t.egress
		}
		if egress, ok := t.others[conn]; ok {
			return egress
		}
		// Follow the existing WithUpstream/NetConn convention one layer at a time
		// so a connection recorded at an intermediate layer remains visible.
		var next net.Conn
		switch wrapper := conn.(type) {
		case common.WithUpstream:
			next, _ = wrapper.Upstream().(net.Conn)
		case interface{ NetConn() net.Conn }:
			next = wrapper.NetConn()
		}
		if reflect.ValueOf(next).Comparable() && next == conn {
			break
		}
		conn = next
	}
	return dnsevents.Egress{}
}
