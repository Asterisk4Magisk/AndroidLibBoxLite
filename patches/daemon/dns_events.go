// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0

package daemon

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/androidcli/dnsevents"
	"github.com/sagernet/sing/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type dnsEventsServer struct {
	UnimplementedDnsEventsServiceServer
	service        *StartedService
	tokenDirectory string
}

func RegisterDNSEvents(server grpc.ServiceRegistrar, service *StartedService, tokenDirectory string) {
	RegisterDnsEventsServiceServer(server, &dnsEventsServer{service: service, tokenDirectory: tokenDirectory})
}

func (s *dnsEventsServer) GetDnsEventsVersion(ctx context.Context, _ *emptypb.Empty) (*wrapperspb.UInt32Value, error) {
	if err := authenticateLocalExtension(ctx, s.tokenDirectory); err != nil {
		return nil, err
	}
	return wrapperspb.UInt32(dnsevents.Version), nil
}

func (s *dnsEventsServer) SubscribeDnsEvents(_ *emptypb.Empty, stream grpc.ServerStreamingServer[wrapperspb.StringValue]) error {
	ctx := stream.Context()
	if err := authenticateLocalExtension(ctx, s.tokenDirectory); err != nil {
		return err
	}
	s.service.serviceAccess.RLock()
	instance := s.service.instance
	running := s.service.serviceStatus.Status == ServiceStatus_STARTED
	s.service.serviceAccess.RUnlock()
	if !running || instance == nil || instance.ctx.Err() != nil {
		return status.Error(codes.FailedPrecondition, "service is not started")
	}
	router := service.FromContext[adapter.DNSRouter](instance.ctx)
	provider, ok := router.(dnsevents.FeedProvider)
	if !ok {
		return status.Error(codes.Unimplemented, "DNS events are unavailable")
	}
	events, unsubscribe, ok := provider.DNSEventFeed().Subscribe()
	if !ok {
		return status.Error(codes.Aborted, "service changed")
	}
	defer unsubscribe()
	if err := stream.SendHeader(metadata.Pairs(dnsevents.VersionHeader, strconv.Itoa(dnsevents.Version))); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case <-instance.ctx.Done():
			return status.Error(codes.Aborted, "service changed")
		case event, ok := <-events:
			if !ok || instance.ctx.Err() != nil {
				return status.Error(codes.Aborted, "service changed")
			}
			data, err := json.Marshal(event)
			if err != nil {
				return status.Error(codes.Internal, "encode DNS event")
			}
			if err := stream.Send(wrapperspb.String(string(data))); err != nil {
				return err
			}
		}
	}
}
