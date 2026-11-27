// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: LGPL-3.0

package dnsevents

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	D "github.com/miekg/dns"
)

const Version = 1
const VersionHeader = "x-asterisk-dns-events-version"
const FormatHeader = "x-asterisk-dns-events-format"
const FormatNDJSON = "ndjson"

type Answer struct {
	Name string `json:"name"`
	Type uint16 `json:"type"`
	Data string `json:"data"`
}

type Event struct {
	Seq          uint64    `json:"seq"`
	Time         int64     `json:"time"`
	Delay        int64     `json:"delay"`
	Qname        string    `json:"qname"`
	Qtype        uint16    `json:"qtype"`
	Status       string    `json:"status"`
	Rcode        *int      `json:"rcode,omitempty"`
	Answers      *[]Answer `json:"answers,omitempty"`
	AnswerSource string    `json:"answerSource,omitempty"`
	Upstream     string    `json:"upstream,omitempty"`
	Route        string    `json:"route,omitempty"`
	OutboundTag  string    `json:"outboundTag,omitempty"`
	UID          *uint32   `json:"uid,omitempty"`
	UIDSource    string    `json:"uidSource,omitempty"`
}

type pendingEvent struct {
	Event
	wireAnswers []D.RR
	origin      Origin
}

func (e pendingEvent) format() Event {
	if e.Rcode != nil {
		answers := make([]Answer, 0, len(e.wireAnswers))
		for _, record := range e.wireAnswers {
			header := record.Header()
			data := answerData(record)
			answers = append(answers, Answer{Name: header.Name, Type: header.Rrtype, Data: data})
		}
		e.Answers = &answers
	}
	e.Upstream = e.origin.upstream()
	if e.UID != nil {
		e.UIDSource = "socket"
	}
	return e.Event
}

func answerData(record D.RR) string {
	switch record := record.(type) {
	case *D.A:
		if record.A == nil {
			return ""
		}
		return record.A.String()
	case *D.AAAA:
		if record.AAAA == nil {
			return ""
		}
		if record.AAAA.To4() != nil {
			return "::ffff:" + record.AAAA.String()
		}
		return record.AAAA.String()
	case *D.TXT:
		if len(record.Txt) == 1 && isPlainTXT(record.Txt[0]) {
			return "\"" + record.Txt[0] + "\""
		}
	}
	return strings.TrimSpace(strings.TrimPrefix(record.String(), record.Header().String()))
}

func isPlainTXT(text string) bool {
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c < ' ' || c > '~' || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

type FeedProvider interface {
	DNSEventFeed() *Feed
}

type Feed struct {
	queue   atomic.Pointer[eventQueue]
	mu      sync.Mutex
	seq     uint64
	closed  bool
	readers map[chan Event]struct{}
}

var observingFeeds atomic.Int32

func ObservationEnabled() bool { return observingFeeds.Load() != 0 }

func (f *Feed) Enabled() bool { return f != nil && f.queue.Load() != nil }

func (f *Feed) StartCollection() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed && f.queue.Load() == nil {
		queue := newEventQueue()
		f.queue.Store(queue)
		observingFeeds.Add(1)
		go queue.run(f.publish)
	}
}

func (f *Feed) Subscribe() ([]Event, <-chan Event, func(), bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	queue := f.queue.Load()
	if queue == nil {
		return nil, nil, nil, false
	}
	reader := make(chan Event, eventQueueCapacity)
	if f.readers == nil {
		f.readers = make(map[chan Event]struct{})
	}
	f.readers[reader] = struct{}{}
	return queue.snapshot(), reader, func() {
		f.mu.Lock()
		delete(f.readers, reader)
		f.mu.Unlock()
	}, true
}

func (f *Feed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.stopQueue()
	for reader := range f.readers {
		close(reader)
	}
	f.readers = nil
}

func (f *Feed) stopQueue() {
	if queue := f.queue.Swap(nil); queue != nil {
		observingFeeds.Add(-1)
		close(queue.done)
	}
}

func (f *Feed) publish(queue *eventQueue, event Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.queue.Load() != queue {
		return
	}
	f.seq++
	event.Seq = f.seq
	queue.remember(event)
	for reader := range f.readers {
		sendLatest(reader, event)
	}
}

type Origin struct {
	Source string
	Scheme string
	Peer   interface{ DNSEventAddress() string }
	Egress
}

type Egress struct {
	Route string
	Tag   string
}

type requestKey struct{}
type attemptKey struct{}

type Observation struct {
	feed     *Feed
	request  *D.Msg
	start    time.Time
	question D.Question
	uid      *uint32
	queue    *eventQueue

	mu     sync.Mutex
	done   bool
	first  *D.Msg
	origin Origin
	others map[*D.Msg]Origin
}

func (f *Feed) Start(ctx context.Context, request *D.Msg, uid *uint32) (context.Context, *Observation) {
	queue := f.queue.Load()
	if queue == nil || len(request.Question) == 0 || request.Response || request.Opcode != D.OpcodeQuery {
		return ctx, nil
	}
	o := &Observation{
		feed: f, request: request, start: time.Now(), question: request.Question[0], uid: uid, queue: queue,
	}
	return context.WithValue(ctx, requestKey{}, o), o
}

func ForRequest(ctx context.Context, request *D.Msg) *Observation {
	if !ObservationEnabled() {
		return nil
	}
	o, _ := ctx.Value(requestKey{}).(*Observation)
	if o != nil && o.request == request {
		return o
	}
	return nil
}

// An internal lookup can inherit the caller's context, but owns a different
// DNS message and must not attribute its resolver or dial to the client query.
func WithAttempt(ctx context.Context, observation *Observation) context.Context {
	if !ObservationEnabled() {
		return ctx
	}
	if Attempt(ctx) != observation {
		ctx = context.WithValue(ctx, attemptKey{}, observation)
	}
	return ctx
}

func Attempt(ctx context.Context) *Observation {
	if !ObservationEnabled() {
		return nil
	}
	o, _ := ctx.Value(attemptKey{}).(*Observation)
	return o
}

func (o *Observation) Record(response *D.Msg, facts Origin) {
	if o == nil || response == nil || !o.feed.Enabled() || !o.mu.TryLock() {
		return
	}
	defer o.mu.Unlock()
	if o.done {
		return
	}
	switch o.first {
	case nil, response:
		o.first = response
		merge(&o.origin, facts)
	default:
		if o.others == nil {
			o.others = make(map[*D.Msg]Origin)
		}
		origin := o.others[response]
		merge(&origin, facts)
		o.others[response] = origin
	}
}

func merge(origin *Origin, facts Origin) {
	if origin.Source == "" {
		origin.Source = facts.Source
	}
	if origin.Peer == nil {
		origin.Peer = facts.Peer
	}
	if origin.Scheme == "" {
		origin.Scheme = facts.Scheme
	}
	if origin.Route == "" {
		origin.Egress = facts.Egress
	}
}

func (o *Observation) Finish(response, wireResponse *D.Msg, err error) {
	if o == nil || o.feed.queue.Load() != o.queue || !o.mu.TryLock() {
		return
	}
	if o.done {
		o.mu.Unlock()
		return
	}
	o.done = true
	var origin Origin
	if response != nil && response == o.first {
		origin = o.origin
	} else if response != nil {
		origin = o.others[response]
	}
	o.first, o.others = nil, nil
	o.mu.Unlock()
	if o.feed.queue.Load() != o.queue || !o.queue.reserve() {
		return
	}
	now := time.Now()
	event := pendingEvent{Event: Event{
		Time: now.UnixMilli(), Delay: now.Sub(o.start).Milliseconds(),
		Qname: o.question.Name, Qtype: o.question.Qtype, Status: "failed", UID: o.uid,
	}}
	switch {
	case wireResponse != nil:
		event.Status = "answered"
		rcode := wireResponse.Rcode
		event.Rcode = &rcode
		event.wireAnswers = snapshotAnswers(wireResponse.Answer)
		event.AnswerSource = origin.Source
		if origin.Source == "upstream" {
			event.origin = origin
			event.Route, event.OutboundTag = origin.Route, origin.Tag
		}
	case errors.Is(err, context.DeadlineExceeded):
		event.Status = "timeout"
	}
	o.queue.events <- event
}

func (o Origin) upstream() string {
	if o.Peer == nil || o.Scheme == "" {
		return ""
	}
	parsed, err := url.Parse(o.Scheme + "://" + o.Peer.DNSEventAddress())
	if err != nil || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}
