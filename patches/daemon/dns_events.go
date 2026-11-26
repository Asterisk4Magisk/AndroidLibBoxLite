// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"time"

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
	history, events, unsubscribe, ok := provider.DNSEventFeed().Subscribe()
	if !ok {
		return status.Error(codes.Aborted, "service changed")
	}
	defer unsubscribe()
	headers := metadata.Pairs(dnsevents.VersionHeader, strconv.Itoa(dnsevents.Version))
	formats := metadata.ValueFromIncomingContext(ctx, dnsevents.FormatHeader)
	batched := len(formats) == 1 && formats[0] == dnsevents.FormatNDJSON
	if batched {
		headers.Set(dnsevents.FormatHeader, dnsevents.FormatNDJSON)
	}
	if err := stream.SendHeader(headers); err != nil {
		return err
	}
	// Returning from the handler closes the RPC and unblocks Send even if
	// the client has stopped reading and exhausted its HTTP/2 window.
	done := make(chan error, 1)
	go func() {
		done <- sendDNSEvents(ctx, instance.ctx, history, events, stream.Send, batched)
	}()
	select {
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	case <-instance.ctx.Done():
		return status.Error(codes.Aborted, "service changed")
	case err := <-done:
		return err
	}
}

func sendDNSEvents(ctx, instance context.Context, history []dnsevents.Event, events <-chan dnsevents.Event, send func(*wrapperspb.StringValue) error, batched bool) error {
	const batchInterval = 100 * time.Millisecond
	const maxBatchRecords = 64
	const batchBytes = 64 * 1024

	var batch bytes.Buffer
	encoder := json.NewEncoder(&batch)
	var timer *time.Timer
	var flushAt <-chan time.Time
	records := 0
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	checkStream := func() error {
		if ctx.Err() != nil {
			return status.FromContextError(ctx.Err()).Err()
		}
		if instance.Err() != nil {
			return status.Error(codes.Aborted, "service changed")
		}
		return nil
	}
	flush := func() error {
		if err := checkStream(); err != nil {
			return err
		}
		if timer != nil {
			timer.Stop()
		}
		flushAt = nil
		err := send(wrapperspb.String(batch.String()))
		batch.Reset()
		records = 0
		return err
	}
	writeEvent := func(event dnsevents.Event) error {
		if !batched {
			if err := checkStream(); err != nil {
				return err
			}
			data, err := json.Marshal(event)
			if err != nil {
				return status.Error(codes.Internal, "encode DNS event")
			}
			return send(wrapperspb.String(string(data)))
		}
		if err := encoder.Encode(event); err != nil {
			return status.Error(codes.Internal, "encode DNS event")
		}
		records++
		if records >= maxBatchRecords || batch.Len() >= batchBytes {
			return flush()
		}
		return nil
	}
	for _, event := range history {
		if err := writeEvent(event); err != nil {
			return err
		}
	}
	if records != 0 {
		if err := flush(); err != nil {
			return err
		}
	}
	history = nil
	for {
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case <-instance.Done():
			return status.Error(codes.Aborted, "service changed")
		case <-flushAt:
			if err := flush(); err != nil {
				return err
			}
		case event, ok := <-events:
			if !ok {
				return status.Error(codes.Aborted, "service changed")
			}
			if err := writeEvent(event); err != nil {
				return err
			}
			if records == 1 {
				if timer == nil {
					timer = time.NewTimer(batchInterval)
				} else {
					timer.Reset(batchInterval)
				}
				flushAt = timer.C
			}
		}
	}
}
