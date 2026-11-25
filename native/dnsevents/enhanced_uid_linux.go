// Copyright 2026, Asterisk4Magisk contributors
// SPDX-License-Identifier: LGPL-3.0

//go:build linux || android

package dnsevents

import (
	"math"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

// AsteriskEnhance publishes DNS ownership in native-endian, atomic uint32 words.
const (
	enhancedUIDLegacy  = 0x31444955
	enhancedUIDMagic   = 0x32444955
	enhancedUIDSlots   = 2048
	enhancedUIDWords   = 6
	enhancedUIDSize    = 4 * (2 + enhancedUIDSlots*enhancedUIDWords)
	enhancedUIDTTL     = 5000
	UnknownEnhancedUID = math.MaxUint32
)

type enhancedUIDObservation struct {
	table  []uint32
	demand *uint32
	legacy bool
}

var enhancedObservation = sync.OnceValue(func() enhancedUIDObservation {
	tableText, demandText, found := strings.Cut(os.Getenv("ASTERISK_DNS_OBSERVATION_FDS"), ":")
	tableFD, tableErr := strconv.Atoi(tableText)
	demandFD, demandErr := strconv.Atoi(demandText)
	if !found || tableErr != nil || demandErr != nil || tableFD < 3 || demandFD < 3 || tableFD == demandFD {
		return enhancedUIDObservation{}
	}
	defer unix.Close(tableFD)
	defer unix.Close(demandFD)
	var info unix.Stat_t
	if unix.Fstat(tableFD, &info) != nil || info.Size != enhancedUIDSize ||
		unix.Fstat(demandFD, &info) != nil || info.Size != 4 {
		return enhancedUIDObservation{}
	}
	memory, err := unix.Mmap(tableFD, 0, enhancedUIDSize, unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return enhancedUIDObservation{}
	}
	words := unsafe.Slice((*uint32)(unsafe.Pointer(&memory[0])), len(memory)/4)
	magic := atomic.LoadUint32(&words[0])
	if magic != enhancedUIDMagic && magic != enhancedUIDLegacy {
		_ = unix.Munmap(memory)
		return enhancedUIDObservation{}
	}
	demand, err := unix.Mmap(demandFD, 0, 4, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		_ = unix.Munmap(memory)
		return enhancedUIDObservation{}
	}
	return enhancedUIDObservation{words, (*uint32)(unsafe.Pointer(&demand[0])), magic == enhancedUIDLegacy}
})

func setEnhancedUIDObservation(enabled bool) {
	if demand := enhancedObservation().demand; demand != nil {
		var value uint32
		if enabled {
			value = 1
		}
		atomic.StoreUint32(demand, value)
	}
}

// EnhancedUID returns the requesting application's UID for an observed system
// query. UnknownEnhancedUID marks a matching but ambiguous observation.
func EnhancedUID(network string, source, destination netip.AddrPort, request []byte) (uint32, bool) {
	observation := enhancedObservation()
	table := observation.table
	if table == nil || !source.IsValid() || source.Port() == 0 ||
		!destination.IsValid() || destination.Port() != 53 || len(request) < 12 {
		return 0, false
	}
	epoch := atomic.LoadUint32(&table[1])
	if epoch == 0 {
		return 0, false
	}
	const basis = 14695981039346656037
	const prime = 1099511628211
	hash := uint64(basis)
	switch network {
	case "tcp":
		hash = (hash ^ 6) * prime
	case "udp":
		hash = (hash ^ 17) * prime
		if !observation.legacy {
			request = request[2:]
		}
	default:
		return 0, false
	}
	for _, endpoint := range [...]netip.AddrPort{source, destination} {
		addr := endpoint.Addr().Unmap()
		if addr.Is4() {
			hash = (hash ^ 4) * prime
		} else {
			hash = (hash ^ 6) * prime
		}
		for _, octet := range addr.AsSlice() {
			hash = (hash ^ uint64(octet)) * prime
		}
		port := endpoint.Port()
		hash = (hash ^ uint64(port>>8)) * prime
		hash = (hash ^ uint64(port&255)) * prime
	}
	for _, octet := range request {
		hash = (hash ^ uint64(octet)) * prime
	}
	entry := table[2+int(hash&(enhancedUIDSlots-1))*enhancedUIDWords:]
	sequence := atomic.LoadUint32(&entry[0])
	if sequence&1 != 0 || atomic.LoadUint32(&entry[1]) != epoch ||
		atomic.LoadUint32(&entry[3]) != uint32(hash) || atomic.LoadUint32(&entry[4]) != uint32(hash>>32) {
		return 0, false
	}
	timestamp := atomic.LoadUint32(&entry[2])
	uid := atomic.LoadUint32(&entry[5])
	var now unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC, &now) != nil ||
		uint32(uint64(now.Sec)*1000+uint64(now.Nsec)/1000000)-timestamp > enhancedUIDTTL ||
		atomic.LoadUint32(&entry[0]) != sequence || atomic.LoadUint32(&table[1]) != epoch {
		return 0, false
	}
	return uid, true
}
