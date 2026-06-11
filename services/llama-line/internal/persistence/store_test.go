package persistence

import (
	"testing"
	"time"
)

func TestEnqueueAppLogNonBlocking(t *testing.T) {
	s := &Store{appLogCh: make(chan AppLogEvent, 1)}
	if !s.EnqueueAppLog(AppLogEvent{Level: "info"}) {
		t.Fatal("expected first enqueue to succeed")
	}
	start := time.Now()
	if s.EnqueueAppLog(AppLogEvent{Level: "warn"}) {
		t.Fatal("expected second enqueue to drop when full")
	}
	if time.Since(start) > 10*time.Millisecond {
		t.Fatal("enqueue blocked")
	}
}

func TestEnqueueAppLogDisabled(t *testing.T) {
	if (&Store{}).EnqueueAppLog(AppLogEvent{Level: "info"}) {
		t.Fatal("expected disabled store to reject enqueue")
	}
}

func TestEnqueueAppLogRejectsAfterCloseStarts(t *testing.T) {
	s := &Store{appLogCh: make(chan AppLogEvent, 1)}
	s.appLogClosed = true
	if s.EnqueueAppLog(AppLogEvent{Level: "info"}) {
		t.Fatal("expected closing store to reject enqueue")
	}
}

func TestDrainAppLogChannelKeepsQueuedEventsOnShutdown(t *testing.T) {
	s := &Store{appLogCh: make(chan AppLogEvent, 4)}
	s.appLogCh <- AppLogEvent{Level: "info", Msg: "one"}
	s.appLogCh <- AppLogEvent{Level: "warn", Msg: "two"}

	batch := s.drainAppLogChannel([]AppLogEvent{{Level: "debug", Msg: "already batched"}})
	if len(batch) != 3 {
		t.Fatalf("got %d events, want 3", len(batch))
	}
	if batch[1].Msg != "one" || batch[2].Msg != "two" {
		t.Fatalf("drained wrong events: %#v", batch)
	}
}

func TestJSONOrEmptyObjectDefaultsRawAudit(t *testing.T) {
	got := string(jsonOrEmptyObject(nil))
	if got != "{}" {
		t.Fatalf("got %q, want {}", got)
	}
}

func TestEnqueueRequestEventNonBlocking(t *testing.T) {
	s := &Store{reqCh: make(chan RequestEvent, 1)}
	if !s.EnqueueRequestEvent(RequestEvent{RequestID: "r1"}) {
		t.Fatal("expected first enqueue to succeed")
	}
	start := time.Now()
	if s.EnqueueRequestEvent(RequestEvent{RequestID: "r2"}) {
		t.Fatal("expected second enqueue to drop when full")
	}
	if time.Since(start) > 10*time.Millisecond {
		t.Fatal("enqueue blocked")
	}
}

func TestEnqueueRequestEventDisabled(t *testing.T) {
	if (&Store{}).EnqueueRequestEvent(RequestEvent{RequestID: "r1"}) {
		t.Fatal("expected disabled store to reject enqueue")
	}
}

func TestEnqueueRequestEventRejectsAfterCloseStarts(t *testing.T) {
	s := &Store{reqCh: make(chan RequestEvent, 1)}
	s.reqClosed = true
	if s.EnqueueRequestEvent(RequestEvent{RequestID: "r1"}) {
		t.Fatal("expected closing store to reject enqueue")
	}
}

func TestDrainRequestEventChannelKeepsQueuedEventsOnShutdown(t *testing.T) {
	s := &Store{reqCh: make(chan RequestEvent, 4)}
	s.reqCh <- RequestEvent{RequestID: "r1"}
	s.reqCh <- RequestEvent{RequestID: "r2"}

	batch := s.drainRequestEventChannel([]RequestEvent{{RequestID: "batched"}})
	if len(batch) != 3 {
		t.Fatalf("got %d events, want 3", len(batch))
	}
	if batch[1].RequestID != "r1" || batch[2].RequestID != "r2" {
		t.Fatalf("drained wrong events: %#v", batch)
	}
}
