// Copyright 2026, Asterisk4Magisk contributors
// SPDX-License-Identifier: LGPL-3.0

// Package flowmeta carries the original socket tuple across a local bridge.
// Wire format: PROXY v2 INET/INET6 STREAM/DGRAM, followed by application TLV
// 0xe0: version (1), 32 ASCII credential bytes, and a big-endian Android UID.
// 0xffffffff means that the receiver must look up the original socket owner.
package flowmeta

import (
	"bytes"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
)

const Username = "__asterisk_flow_v1"
const UnknownUID uint32 = 0xffffffff
const MaxHeader = 92

var signature = []byte("\r\n\r\n\x00\r\nQUIT\n")
var ErrHeader = errors.New("invalid flow metadata")

type Header struct {
	Source, Destination netip.AddrPort
	UDP                 bool
	UID                 uint32
}

func (h Header) Addr(source bool) net.Addr {
	a := h.Destination
	if source {
		a = h.Source
	}
	if h.UDP {
		return net.UDPAddrFromAddrPort(a)
	}
	return net.TCPAddrFromAddrPort(a)
}

func (h Header) Reverse(source netip.AddrPort) Header {
	return Header{Source: source, Destination: h.Source, UDP: h.UDP, UID: UnknownUID}
}

func (h Header) Marshal(token string) ([]byte, error) {
	b := make([]byte, MaxHeader)
	n, err := h.MarshalTo(b, token)
	if err != nil {
		return nil, err
	}
	return b[:n], nil
}

// MarshalTo writes one header into caller-owned storage without allocating.
func (h Header) MarshalTo(b []byte, token string) (int, error) {
	if len(token) != 32 || !h.Source.IsValid() || !h.Destination.IsValid() || h.Source.Port() == 0 || h.Destination.Port() == 0 {
		return 0, ErrHeader
	}
	src, dst := h.Source.Addr().Unmap(), h.Destination.Addr().Unmap()
	if src.Is4() != dst.Is4() {
		return 0, ErrHeader
	}
	n, family := 36, byte(0x20)
	if src.Is4() {
		n, family = 12, 0x10
	}
	if len(b) < 16+n+40 {
		return 0, io.ErrShortBuffer
	}
	copy(b, signature)
	b[12], b[13] = 0x21, family|1
	if h.UDP {
		b[13] = family | 2
	}
	binary.BigEndian.PutUint16(b[14:], uint16(n+40))
	copy(b[16:], src.AsSlice())
	copy(b[16+(n-4)/2:], dst.AsSlice())
	binary.BigEndian.PutUint16(b[16+n-4:], h.Source.Port())
	binary.BigEndian.PutUint16(b[16+n-2:], h.Destination.Port())
	t := b[16+n:]
	t[0], t[1], t[2], t[3] = 0xe0, 0, 37, 1
	copy(t[4:36], token)
	binary.BigEndian.PutUint32(t[36:], h.UID)
	return 16 + n + 40, nil
}

func Parse(b []byte, token string, udp bool) (Header, int, error) {
	var h Header
	if len(b) < 16 || !bytes.Equal(b[:12], signature) || b[12] != 0x21 {
		return h, 0, ErrHeader
	}
	n, width := 12, 4
	if b[13]>>4 == 2 {
		n, width = 36, 16
	} else if b[13]>>4 != 1 {
		return h, 0, ErrHeader
	}
	protocol := byte(1)
	if udp {
		protocol = 2
	}
	if b[13]&15 != protocol || int(binary.BigEndian.Uint16(b[14:])) != n+40 || len(b) < 16+n+40 {
		return h, 0, ErrHeader
	}
	t := b[16+n : 16+n+40]
	if t[0] != 0xe0 || t[1] != 0 || t[2] != 37 || t[3] != 1 || len(token) != 32 || subtle.ConstantTimeCompare(t[4:36], []byte(token)) != 1 {
		return h, 0, ErrHeader
	}
	src, _ := netip.AddrFromSlice(b[16 : 16+width])
	dst, _ := netip.AddrFromSlice(b[16+width : 16+2*width])
	h = Header{Source: netip.AddrPortFrom(src.Unmap(), binary.BigEndian.Uint16(b[16+n-4:])), Destination: netip.AddrPortFrom(dst.Unmap(), binary.BigEndian.Uint16(b[16+n-2:])), UDP: udp, UID: binary.BigEndian.Uint32(t[36:])}
	if h.Source.Port() == 0 || h.Destination.Port() == 0 {
		return Header{}, 0, ErrHeader
	}
	return h, 16 + n + 40, nil
}

func Read(r io.Reader, token string) (Header, error) {
	var b [MaxHeader]byte
	if _, err := io.ReadFull(r, b[:16]); err != nil {
		return Header{}, err
	}
	n := int(binary.BigEndian.Uint16(b[14:]))
	if n != 52 && n != 76 {
		return Header{}, ErrHeader
	}
	if _, err := io.ReadFull(r, b[16:16+n]); err != nil {
		return Header{}, err
	}
	h, _, err := Parse(b[:16+n], token, false)
	return h, err
}

// ParseAuth authenticates the credential against the private inbound's account.
func ParseAuth(b []byte, udp bool, verify func(string) bool) (Header, int, string, error) {
	if len(b) < 16 {
		return Header{}, 0, "", ErrHeader
	}
	n := int(binary.BigEndian.Uint16(b[14:]))
	if (n != 52 && n != 76) || len(b) < 16+n {
		return Header{}, 0, "", ErrHeader
	}
	token := string(b[16+n-36 : 16+n-4])
	if !verify(token) {
		return Header{}, 0, "", ErrHeader
	}
	h, size, err := Parse(b, token, udp)
	return h, size, token, err
}

func ReadAuth(r io.Reader, verify func(string) bool) (Header, error) {
	var b [MaxHeader]byte
	if _, err := io.ReadFull(r, b[:16]); err != nil {
		return Header{}, err
	}
	n := int(binary.BigEndian.Uint16(b[14:]))
	if n != 52 && n != 76 {
		return Header{}, ErrHeader
	}
	if _, err := io.ReadFull(r, b[16:16+n]); err != nil {
		return Header{}, err
	}
	h, _, _, err := ParseAuth(b[:16+n], false, verify)
	return h, err
}

// OwnerAddr preserves an explicit UID without changing the printable tuple.
type OwnerAddr struct {
	net.Addr
	UID uint32
}

func (a OwnerAddr) FlowUID() uint32   { return a.UID }
func (a OwnerAddr) RawAddr() net.Addr { return a.Addr }
