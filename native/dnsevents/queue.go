// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: LGPL-3.0

package dnsevents

import D "github.com/miekg/dns"

const eventQueueCapacity = 64

func snapshotAnswers(records []D.RR) []D.RR {
	const maxRecords = 64
	const maxBytes = 8 * 1024
	if len(records) == 0 {
		return nil
	}
	answers := make([]D.RR, 0, min(len(records), maxRecords))
	remaining := maxBytes
	for _, record := range records[:cap(answers)] {
		size := D.Len(record)
		if size > remaining {
			break
		}
		remaining -= size
		answers = append(answers, D.Copy(record))
	}
	return answers
}

// This queue belongs to one period of continuous observation. Reserve capacity
// before copying the DNS response, and retire the queue on the last unsubscribe.
type eventQueue struct {
	events   chan Event
	reserved chan struct{}
	done     chan struct{}
}

func newEventQueue() *eventQueue {
	return &eventQueue{
		events:   make(chan Event, eventQueueCapacity),
		reserved: make(chan struct{}, eventQueueCapacity),
		done:     make(chan struct{}),
	}
}

func (q *eventQueue) reserve() bool {
	select {
	case q.reserved <- struct{}{}:
		return true
	default:
		return false
	}
}

func (q *eventQueue) run(publish func(*eventQueue, Event)) {
	for {
		select {
		case <-q.done:
			return
		case event := <-q.events:
			publish(q, event.format())
			<-q.reserved
		}
	}
}

func sendLatest(listener chan Event, event Event) {
	select {
	case listener <- event:
	default:
		select {
		case <-listener:
		default:
		}
		listener <- event
	}
}
