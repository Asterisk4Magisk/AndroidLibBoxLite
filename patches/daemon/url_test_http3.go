// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0
//
// HTTP/3 transport for URLTestWithOptions. Mirrors husi
// libcore/urltest/http3.go.

//go:build with_quic

package daemon

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
)

func urlTestNewHTTP3Transport(conn net.Conn, tlsConfig *tls.Config) (http.RoundTripper, error) {
	return &http3.Transport{
		TLSClientConfig: tlsConfig,
		Dial: func(ctx context.Context, _ string, tlsConfig *tls.Config, quicConfig *quic.Config) (*quic.Conn, error) {
			return quic.DialEarlyConn(ctx, conn, tlsConfig, quicConfig)
		},
	}, nil
}
