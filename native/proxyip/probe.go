// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0

package proxyip

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	TokenHeader  = "X-Asterisk-Probe-Token"
	WorkTimeout  = 10 * time.Second
	maxBody      = 16 * 1024
	maxRedirects = 10
)

type Request struct {
	OutboundTag string `json:"outboundTag"`
	URL         string `json:"url"`
}

func (r Request) Valid() bool {
	endpoint, err := url.Parse(r.URL)
	return r.OutboundTag != "" && err == nil && validEndpoint(endpoint)
}

func validEndpoint(endpoint *url.URL) bool {
	return endpoint.Scheme == "https" && endpoint.Hostname() != "" && endpoint.User == nil
}

type Sample struct {
	OutboundTag string  `json:"outboundTag"`
	Status      string  `json:"status"`
	Response    *string `json:"response,omitempty"`
}

type readOnlyKey struct{}

func ReadOnly(ctx context.Context) bool {
	return ctx.Value(readOnlyKey{}) == true
}

var ErrBlocked = errors.New("probe blocked by outbound")

func Measure(ctx context.Context, request Request, dial func(context.Context, string) (net.Conn, error), configureTLS func(*http.Transport)) Sample {
	ctx, cancel := context.WithTimeout(context.WithValue(ctx, readOnlyKey{}, true), WorkTimeout)
	defer cancel()
	sample := Sample{OutboundTag: request.OutboundTag, Status: "unreachable"}
	transport := &http.Transport{
		DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
			// net/http can detach a transport dial from the request lifetime.
			return dial(ctx, address)
		},
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: maxBody,
	}
	if configureTLS != nil {
		configureTLS(transport)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects || !validEndpoint(next.URL) {
			return http.ErrUseLastResponse
		}
		next.Header.Del("Referer")
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, request.URL, nil)
	if err != nil {
		return sample
	}
	resp, err := client.Do(req)
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			sample.Status = "timeout"
		case errors.Is(err, ErrBlocked):
			sample.Status = "blocked"
		}
		return sample
	}
	defer resp.Body.Close()
	sample.Status = "invalid_response"
	if resp.StatusCode != http.StatusOK {
		return sample
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		sample.Status = "timeout"
		return sample
	}
	if err != nil || len(body) > maxBody || !utf8.Valid(body) {
		return sample
	}
	response := string(body)
	sample.Response = &response
	sample.Status = "ok"
	return sample
}

func Loopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

func ValidToken(directory, token string) bool {
	provided, err := hex.DecodeString(token)
	if err != nil || len(provided) != 32 {
		return false
	}
	file, err := os.Open(filepath.Join(directory, "proxy-ip-probe.token"))
	if err != nil {
		return false
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 67))
	if err != nil {
		return false
	}
	expected, err := hex.DecodeString(strings.TrimSpace(string(body)))
	return err == nil && len(expected) == 32 && subtle.ConstantTimeCompare(provided, expected) == 1
}
