// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0

package daemon

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/androidcli/proxyip"
	"github.com/sagernet/sing-box/common/dialer"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type proxyIpProbeServer struct {
	UnimplementedProxyIpProbeServiceServer
	service        *StartedService
	tokenDirectory string
}

func RegisterProxyIPProbe(server grpc.ServiceRegistrar, service *StartedService, tokenDirectory string) {
	RegisterProxyIpProbeServiceServer(server, &proxyIpProbeServer{service: service, tokenDirectory: tokenDirectory})
}

func authenticateLocalExtension(ctx context.Context, tokenDirectory string) error {
	remote, ok := peer.FromContext(ctx)
	if !ok || remote.Addr == nil {
		return status.Error(codes.PermissionDenied, "missing local peer")
	}
	_, unix := remote.Addr.(*net.UnixAddr)
	tokens := metadata.ValueFromIncomingContext(ctx, proxyip.TokenHeader)
	if !(unix || proxyip.Loopback(remote.Addr.String())) || len(tokens) != 1 || !proxyip.ValidToken(tokenDirectory, tokens[0]) {
		return status.Error(codes.PermissionDenied, "invalid probe credentials")
	}
	return nil
}

func (s *proxyIpProbeServer) ProbeProxyIp(ctx context.Context, request *ProxyIpProbeRequest) (*ProxyIpProbeResponse, error) {
	if err := authenticateLocalExtension(ctx, s.tokenDirectory); err != nil {
		return nil, err
	}

	probe := proxyip.Request{OutboundTag: request.GetOutboundTag(), URL: request.GetUrl()}
	if !probe.Valid() {
		return nil, status.Error(codes.InvalidArgument, "a group tag and HTTPS URL are required")
	}
	s.service.serviceAccess.RLock()
	instance := s.service.instance
	s.service.serviceAccess.RUnlock()
	if instance == nil || instance.ctx.Err() != nil {
		return nil, status.Error(codes.FailedPrecondition, "service is not started")
	}
	outbound, err := resolveOutbound(instance, probe.OutboundTag)
	if err != nil {
		return nil, err
	}
	if _, group := outbound.(adapter.OutboundGroup); !group {
		return nil, status.Error(codes.NotFound, "outbound group not found")
	}

	detour := dialer.NewResolveDialer(instance.ctx, outbound, true, "", adapter.DNSQueryOptions{}, 0)
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(instance.ctx, cancel)()
	sample := proxyip.Measure(probeCtx, probe, func(ctx context.Context, address string) (net.Conn, error) {
		destination := M.ParseSocksaddr(address)
		if destination.IsDomain() {
			// Keep the domain as the group selection key after DNS resolves it to an IP.
			ctx = adapter.WithContext(ctx, &adapter.InboundContext{Domain: destination.Fqdn})
		}
		return detour.DialContext(ctx, N.NetworkTCP, destination)
	}, func(transport *http.Transport) {
		transport.TLSClientConfig = &tls.Config{
			Time:    ntp.TimeFuncFromContext(instance.ctx),
			RootCAs: adapter.RootPoolFromContext(instance.ctx),
		}
	})
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if instance.ctx.Err() != nil {
		return nil, status.Error(codes.Aborted, "service changed during probe")
	}
	response := &ProxyIpProbeResponse{OutboundTag: sample.OutboundTag}
	switch sample.Status {
	case "ok":
		response.Status = ProxyIpStatus_PROXY_IP_STATUS_OK
		response.Response = *sample.Response
	case "timeout":
		response.Status = ProxyIpStatus_PROXY_IP_STATUS_TIMEOUT
	case "invalid_response":
		response.Status = ProxyIpStatus_PROXY_IP_STATUS_INVALID_RESPONSE
	case "blocked":
		response.Status = ProxyIpStatus_PROXY_IP_STATUS_BLOCKED
	default:
		response.Status = ProxyIpStatus_PROXY_IP_STATUS_UNREACHABLE
	}
	return response, nil
}
