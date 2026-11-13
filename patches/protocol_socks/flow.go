// Copyright 2026, Asterisk4Magisk contributors
// SPDX-License-Identifier: LGPL-3.0

package socks

import (
	"context"
	"net"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/androidcli/flowmeta"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/udpnat2"
)

type flowInbound struct {
	inbound.Adapter
	ctx      context.Context
	router   adapter.Router
	logger   log.ContextLogger
	listener *listener.Listener
	nat      *udpnat.Service
	token    string
}

type flowPacketInfo struct {
	header flowmeta.Header
	relay  M.Socksaddr
}

type flowContextKey struct{}

func newFlowInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.SocksInboundOptions, token string) (adapter.Inbound, error) {
	if len(token) != 32 {
		return nil, flowmeta.ErrHeader
	}
	i := &flowInbound{
		Adapter: inbound.NewAdapter("socks", tag),
		ctx:     ctx,
		router:  router,
		logger:  logger,
		token:   token,
	}
	i.nat = udpnat.New(i, i.prepare, time.Minute, false)
	i.listener = listener.New(listener.Options{
		Context:           ctx,
		Logger:            logger,
		Network:           []string{"tcp", "udp"},
		Listen:            options.ListenOptions,
		ConnectionHandler: i,
		PacketHandler:     i,
	})
	return i, nil
}

func (i *flowInbound) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if err := i.listener.Start(); err != nil {
		return err
	}
	scope.Add(i.listener.Close)
	scope.Add(func() error {
		i.nat.Purge()
		return nil
	})
	return nil
}

func (i *flowInbound) InterfaceUpdated(context.Context) { i.nat.Purge() }

func (i *flowInbound) metadata(h flowmeta.Header) adapter.InboundContext {
	m := adapter.InboundContext{
		Inbound:           i.Tag(),
		InboundType:       i.Type(),
		Source:            M.SocksaddrFromNet(h.Addr(true)),
		Destination:       M.SocksaddrFromNet(h.Addr(false)),
		OriginDestination: M.SocksaddrFromNet(h.Addr(false)),
	}
	if h.UID != flowmeta.UnknownUID {
		m.ProcessInfo = &adapter.ConnectionOwner{UserId: int32(h.UID)}
	}
	return m
}

func (i *flowInbound) NewConnection(ctx context.Context, conn net.Conn, _ adapter.InboundContext, onClose N.CloseHandlerFunc) {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	h, err := flowmeta.Read(conn, i.token)
	if err != nil {
		N.CloseOnHandshakeFailure(conn, onClose, err)
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	i.router.RouteConnectionEx(ctx, conn, i.metadata(h), onClose)
}

func (i *flowInbound) NewPacket(buffer *buf.Buffer, source M.Socksaddr) {
	h, n, err := flowmeta.Parse(buffer.Bytes(), i.token, true)
	if err != nil {
		return
	}
	i.nat.NewPacket([][]byte{buffer.Bytes()[n:]}, M.SocksaddrFromNet(h.Addr(true)), M.SocksaddrFromNet(h.Addr(false)), flowPacketInfo{h, source})
}

func (i *flowInbound) NewPacketBatch(buffers []*buf.Buffer, sources []M.Socksaddr) {
	for n, b := range buffers {
		i.NewPacket(b, sources[n])
		b.Release()
	}
}

func (i *flowInbound) prepare(source, destination M.Socksaddr, data any) (bool, context.Context, N.PacketWriter, N.CloseHandlerFunc) {
	info := data.(flowPacketInfo)
	return true, context.WithValue(log.ContextWithNewID(i.ctx), flowContextKey{}, info.header), &flowPacketWriter{i, info}, nil
}

func (i *flowInbound) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	h := ctx.Value(flowContextKey{}).(flowmeta.Header)
	i.router.RoutePacketConnectionEx(ctx, conn, i.metadata(h), onClose)
}

type flowPacketWriter struct {
	i    *flowInbound
	info flowPacketInfo
}

func (w *flowPacketWriter) WritePacket(buffer *buf.Buffer, source M.Socksaddr) error {
	defer buffer.Release()
	output := buf.NewSize(flowmeta.MaxHeader + buffer.Len())
	n, err := w.info.header.Reverse(source.AddrPort()).MarshalTo(output.Extend(flowmeta.MaxHeader), w.i.token)
	if err != nil {
		output.Release()
		return err
	}
	output.Truncate(n)
	_, _ = output.Write(buffer.Bytes())
	return w.i.listener.PacketWriter().WritePacket(output, w.info.relay)
}
