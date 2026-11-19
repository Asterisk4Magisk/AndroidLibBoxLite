// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0

package libbox

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/sagernet/sing-box/common/androidcli/proxyip"
	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type ProxyIpProbeHandler interface {
	OnResult(resultJSON string)
	OnError(code string)
}

type ProxyIpProbeSession struct {
	cancel context.CancelFunc
}

func (s *ProxyIpProbeSession) Close() {
	s.cancel()
}

// ProbeProxyIp starts a cancellable diagnostic on the connected command client.
func (c *CommandClient) ProbeProxyIp(outboundTag string, url string, token string, handler ProxyIpProbeHandler) (*ProxyIpProbeSession, error) {
	request := proxyip.Request{OutboundTag: outboundTag, URL: url}
	if !request.Valid() || handler == nil {
		return nil, errors.New("a group tag, HTTPS URL and result handler are required")
	}
	if c.remote != nil && !proxyip.Loopback(c.remote.target) {
		return nil, errors.New("proxy IP probe requires a local command endpoint")
	}
	c.clientAccess.RLock()
	connection, parent := c.grpcConn, c.ctx
	c.clientAccess.RUnlock()
	if connection == nil || parent.Err() != nil {
		return nil, errors.New("proxy IP probe requires a connected command client")
	}
	ctx, cancel := context.WithCancel(parent)
	ctx = metadata.AppendToOutgoingContext(ctx, proxyip.TokenHeader, token)
	input := &daemon.ProxyIpProbeRequest{OutboundTag: outboundTag, Url: url}
	go func() {
		defer cancel()
		response, err := daemon.NewProxyIpProbeServiceClient(connection).ProbeProxyIp(ctx, input)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			handler.OnError(proxyIpErrorCode(status.Code(err)))
			return
		}
		sample, ok := proxyIpResult(request, response)
		if !ok {
			handler.OnError("TRANSPORT_FAILURE")
			return
		}
		encoded, err := json.Marshal(sample)
		if err != nil {
			handler.OnError("TRANSPORT_FAILURE")
			return
		}
		handler.OnResult(string(encoded))
	}()
	return &ProxyIpProbeSession{cancel: cancel}, nil
}

func proxyIpResult(request proxyip.Request, response *daemon.ProxyIpProbeResponse) (proxyip.Sample, bool) {
	sample := proxyip.Sample{OutboundTag: response.OutboundTag}
	if response.OutboundTag != request.OutboundTag {
		return sample, false
	}
	switch response.Status {
	case daemon.ProxyIpStatus_PROXY_IP_STATUS_OK:
		sample.Status = "ok"
		sample.Response = &response.Response
	case daemon.ProxyIpStatus_PROXY_IP_STATUS_TIMEOUT:
		sample.Status = "timeout"
	case daemon.ProxyIpStatus_PROXY_IP_STATUS_INVALID_RESPONSE:
		sample.Status = "invalid_response"
	case daemon.ProxyIpStatus_PROXY_IP_STATUS_BLOCKED:
		sample.Status = "blocked"
	case daemon.ProxyIpStatus_PROXY_IP_STATUS_UNREACHABLE:
		sample.Status = "unreachable"
	default:
		return sample, false
	}
	return sample, true
}

func proxyIpErrorCode(code codes.Code) string {
	switch code {
	case codes.Unimplemented:
		return "UNSUPPORTED"
	case codes.FailedPrecondition:
		return "NOT_RUNNING"
	case codes.NotFound:
		return "OUTBOUND_NOT_FOUND"
	case codes.InvalidArgument:
		return "INVALID_ARGUMENT"
	case codes.PermissionDenied, codes.Unauthenticated:
		return "PERMISSION_DENIED"
	case codes.Aborted:
		return "RUNTIME_CHANGED"
	default:
		return "TRANSPORT_FAILURE"
	}
}
