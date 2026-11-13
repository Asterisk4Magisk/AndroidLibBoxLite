// Copyright 2026, Asterisk4Magisk contributors
// SPDX-License-Identifier: LGPL-3.0

//go:build linux && cgo

package flowmeta

/*
#include "owner_diag.h"
*/
import "C"

import (
	"errors"
	"net/netip"
	"sync"
	"syscall"
	"unsafe"
)

var socketQueries [4]struct {
	sync.Mutex
	query C.struct_flow_owner_query
}

// FindSocket queries the captured tuple, never the bridge's socket. Queries
// share descriptors and buffers; attribution is only cached by the flow owner.
func FindSocket(network string, source, destination netip.AddrPort) (uid, inode uint32, err error) {
	udp, index := C.int(0), 0
	if network == "udp" {
		udp, index = 1, 2
	} else if network != "tcp" {
		return 0, 0, syscall.EPROTONOSUPPORT
	}
	if !source.IsValid() {
		return 0, 0, syscall.EINVAL
	}
	if !source.Addr().Unmap().Is4() {
		index++
	}
	q := &socketQueries[index]
	q.Lock()
	defer q.Unlock()
	src := source.Addr().As16()
	var dst [16]byte
	if destination.IsValid() {
		dst = destination.Addr().As16()
	}
	var owner, socket C.uint32_t
	result := C.flow_owner_lookup(&q.query, udp,
		(*C.uchar)(unsafe.Pointer(&src[0])), C.uint16_t(source.Port()),
		(*C.uchar)(unsafe.Pointer(&dst[0])), C.uint16_t(destination.Port()), &owner, &socket)
	if result == 0 {
		return uint32(owner), uint32(socket), nil
	}
	err = syscall.Errno(-result)
	if errors.Is(err, syscall.EEXIST) {
		err = errAmbiguousOwner
	}
	return 0, 0, err
}
