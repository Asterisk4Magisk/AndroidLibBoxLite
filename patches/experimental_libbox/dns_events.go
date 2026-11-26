// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0

package libbox

import (
	"context"
	"errors"
	"strconv"

	"github.com/sagernet/sing-box/common/androidcli/dnsevents"
	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type DnsEventsHandler interface {
	OnReady()
	// OnEvent receives a JSON record or an NDJSON batch.
	OnEvent(payload string)
	OnError(code string)
}

type DnsEventsSession struct {
	cancel context.CancelFunc
}

func (s *DnsEventsSession) Close() { s.cancel() }

func (c *CommandClient) dnsEventsClient() (daemon.DnsEventsServiceClient, context.Context, error) {
	connection, parent, err := c.localExtensionConnection()
	if err != nil {
		return nil, nil, err
	}
	return daemon.NewDnsEventsServiceClient(connection), parent, nil
}

// GetDnsEventsVersion returns 0 when the connected core predates DNS events.
func (c *CommandClient) GetDnsEventsVersion(token string) (int32, error) {
	client, parent, err := c.dnsEventsClient()
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(parent, commandClientProbeTimeout)
	defer cancel()
	ctx = withLocalExtensionToken(ctx, token)
	response, err := client.GetDnsEventsVersion(ctx, &emptypb.Empty{})
	if status.Code(err) == codes.Unimplemented {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return int32(response.Value), nil
}

func (c *CommandClient) SubscribeDnsEvents(token string, handler DnsEventsHandler) (*DnsEventsSession, error) {
	return c.subscribeDnsEvents(token, handler, false)
}

// SubscribeDnsEventBatches sends NDJSON to OnEvent. A legacy core sends one
// JSON record per callback, which is also a valid single-line batch.
func (c *CommandClient) SubscribeDnsEventBatches(token string, handler DnsEventsHandler) (*DnsEventsSession, error) {
	return c.subscribeDnsEvents(token, handler, true)
}

func (c *CommandClient) subscribeDnsEvents(token string, handler DnsEventsHandler, batched bool) (*DnsEventsSession, error) {
	if handler == nil {
		return nil, errors.New("a DNS events handler is required")
	}
	client, parent, err := c.dnsEventsClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	ctx = withLocalExtensionToken(ctx, token)
	if batched {
		ctx = metadata.AppendToOutgoingContext(ctx, dnsevents.FormatHeader, dnsevents.FormatNDJSON)
	}
	go func() {
		defer cancel()
		err := readDnsEvents(ctx, client, handler)
		if err != nil && ctx.Err() == nil {
			handler.OnError(commandRPCErrorCode(status.Code(err)))
		}
	}()
	return &DnsEventsSession{cancel: cancel}, nil
}

func readDnsEvents(ctx context.Context, client daemon.DnsEventsServiceClient, handler DnsEventsHandler) error {
	stream, err := client.SubscribeDnsEvents(ctx, &emptypb.Empty{})
	if err != nil {
		return err
	}
	headers, err := stream.Header()
	if err != nil {
		return err
	}
	versions := headers.Get(dnsevents.VersionHeader)
	if len(versions) != 1 || versions[0] != strconv.Itoa(dnsevents.Version) {
		return status.Error(codes.Unimplemented, "unsupported DNS event stream version")
	}
	if ctx.Err() == nil {
		handler.OnReady()
	}
	for ctx.Err() == nil {
		message, err := stream.Recv()
		if err != nil {
			return err
		}
		if ctx.Err() == nil {
			handler.OnEvent(message.Value)
		}
	}
	return ctx.Err()
}
