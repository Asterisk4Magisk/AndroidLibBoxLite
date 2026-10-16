// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0
//
// HTTP/3 stub for URLTestWithOptions builds without QUIC. Mirrors husi
// libcore/urltest/http3_stub.go.

//go:build !with_quic

package daemon

import (
	"crypto/tls"
	"net"
	"net/http"

	C "github.com/sagernet/sing-box/constant"
)

func urlTestNewHTTP3Transport(_ net.Conn, _ *tls.Config) (http.RoundTripper, error) {
	return nil, C.ErrQUICNotIncluded
}
