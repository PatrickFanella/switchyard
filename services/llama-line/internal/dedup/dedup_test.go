package dedup_test

import (
	"errors"
	"net/http"
	"testing"

	"git.subcult.tv/PatrickFanella/llama-line/internal/dedup"
	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
)

func TestDeduplicator_NoSubscriber_WhenNoInFlight(t *testing.T) {
	d := dedup.NewDeduplicator()
	w, ok := d.Subscribe("abc")
	if ok || w != nil {
		t.Fatal("expected no subscriber when no in-flight entry")
	}
}

func TestDeduplicator_SubscribeAfterRegister(t *testing.T) {
	d := dedup.NewDeduplicator()
	d.Register("abc")
	w, ok := d.Subscribe("abc")
	if !ok || w == nil {
		t.Fatal("expected subscriber after Register")
	}
}

func TestDeduplicator_PublishError(t *testing.T) {
	d := dedup.NewDeduplicator()
	d.Register("abc")
	w, _ := d.Subscribe("abc")

	err := errors.New("ollama down")
	d.Publish("abc", queue.Result{Err: err}, nil)

	result := <-w.ResponseCh
	if result.Err != err {
		t.Fatalf("expected err %v, got %v", err, result.Err)
	}
}

func TestDeduplicator_PublishSuccess(t *testing.T) {
	d := dedup.NewDeduplicator()
	d.Register("abc")
	w1, _ := d.Subscribe("abc")
	w2, _ := d.Subscribe("abc")

	// Build a minimal fake response.
	fakeResp := &http.Response{
		StatusCode: 200,
		Header:     make(http.Header),
	}
	body := []byte(`{"id":"chatcmpl-1"}`)
	d.Publish("abc", queue.Result{Resp: fakeResp}, body)

	for i, w := range []*dedup.Waiter{w1, w2} {
		r := <-w.ResponseCh
		if r.Err != nil {
			t.Fatalf("waiter %d: unexpected error: %v", i, r.Err)
		}
		if r.Resp == nil {
			t.Fatalf("waiter %d: expected non-nil response", i)
		}
	}
}

func TestDeduplicator_DoublePublish_Noop(t *testing.T) {
	d := dedup.NewDeduplicator()
	d.Register("abc")
	d.Publish("abc", queue.Result{Err: errors.New("e")}, nil)
	// Second publish on same hash is a no-op — should not panic.
	d.Publish("abc", queue.Result{Err: errors.New("e2")}, nil)
}

func TestDeduplicator_NoSubscriber_AfterPublish(t *testing.T) {
	d := dedup.NewDeduplicator()
	d.Register("abc")
	d.Publish("abc", queue.Result{}, nil)
	// After publish the entry is removed; new subscribe should fail.
	_, ok := d.Subscribe("abc")
	if ok {
		t.Fatal("expected no subscriber after publish removes entry")
	}
}
