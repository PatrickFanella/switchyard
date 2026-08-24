package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/runtimeconfig"
)

func TestWebhookRuntimeConfigBounds(t *testing.T) {
	t.Setenv("SWITCHYARD_WEBHOOK_MAX_CONCURRENCY", "0")
	if _, err := webhookIntEnv("SWITCHYARD_WEBHOOK_MAX_CONCURRENCY", 1, 1024); err == nil {
		t.Fatal("expected zero concurrency rejection")
	}
	t.Setenv("SWITCHYARD_WEBHOOK_TIMEOUT", "31s")
	if _, err := webhookDurationEnv("SWITCHYARD_WEBHOOK_TIMEOUT", 30*time.Second); err == nil {
		t.Fatal("expected excessive timeout rejection")
	}
	maximum := webhookWriteTimeout - webhookResponseMargin
	if webhookResponseMargin <= 0 || maximum >= webhookWriteTimeout {
		t.Fatalf("response margin=%s maximum=%s write timeout=%s", webhookResponseMargin, maximum, webhookWriteTimeout)
	}
	if webhookReadTimeout > maximum || webhookWriteTimeout-webhookReadTimeout < webhookResponseMargin {
		t.Fatalf("read timeout=%s does not reserve response margin=%s below write timeout=%s", webhookReadTimeout, webhookResponseMargin, webhookWriteTimeout)
	}
	if err := validateWebhookAdmissionTimeout(maximum); err != nil {
		t.Fatalf("maximum admission timeout rejected: %v", err)
	}
	t.Setenv("SWITCHYARD_WEBHOOK_TIMEOUT", (maximum + time.Nanosecond).String())
	if _, err := webhookDurationEnv("SWITCHYARD_WEBHOOK_TIMEOUT", maximum); err == nil {
		t.Fatal("runtime config accepted timeout without response margin")
	}
	for _, timeout := range []time.Duration{maximum + time.Nanosecond, webhookWriteTimeout} {
		if err := validateWebhookAdmissionTimeout(timeout); err == nil {
			t.Fatalf("timeout %s must reserve response margin", timeout)
		}
	}
	t.Setenv("SWITCHYARD_WEBHOOK_TIMEOUT", "")
	value, err := webhookDurationEnv("SWITCHYARD_WEBHOOK_TIMEOUT", maximum)
	if err != nil || validateWebhookAdmissionTimeout(value) != nil {
		t.Fatalf("default timeout contract: value=%s err=%v", value, err)
	}
	if value != runtimeconfig.Duration("SWITCHYARD_WEBHOOK_TIMEOUT") {
		t.Fatalf("runtime default mismatch: env=%s defaults=%s", value, runtimeconfig.Duration("SWITCHYARD_WEBHOOK_TIMEOUT"))
	}
}

func TestWebhookListenerSemanticCollision(t *testing.T) {
	for _, test := range []struct {
		first, second string
		want          bool
	}{
		{":8082", "0.0.0.0:8082", true},
		{"127.0.0.1:8082", "127.0.0.1:8082", true},
		{"127.0.0.1:8082", "127.0.0.1:8080", false},
		{"127.0.0.1:8082", "10.0.0.1:8082", false},
	} {
		if got := listenerAddressesOverlap(test.first, test.second); got != test.want {
			t.Errorf("overlap(%q,%q)=%t want=%t", test.first, test.second, got, test.want)
		}
	}
}

func TestBindFailurePrecedesWebhookAuthActivation(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	activation := &testWebhookActivator{}
	servers := []*http.Server{{Addr: "127.0.0.1:0"}, {Addr: occupied.Addr().String()}}
	var firstAddress string
	listen := func(_ context.Context, network, address string) (net.Listener, error) {
		listener, err := net.Listen(network, address)
		if err == nil && firstAddress == "" {
			firstAddress = listener.Addr().String()
		}
		return listener, err
	}
	if _, err := bindAndActivateServers(t.Context(), servers, activation, listen); err == nil {
		t.Fatal("expected bind conflict")
	}
	if activation.calls != 0 {
		t.Fatalf("auth activated %d times before all binds completed", activation.calls)
	}
	rebound, err := net.Listen("tcp", firstAddress)
	if err != nil {
		t.Fatalf("earlier listener not closed after bind conflict: %v", err)
	}
	rebound.Close()
}

func TestWebhookAuthFailureClosesPreboundListeners(t *testing.T) {
	want := errors.New("replace auth")
	activation := &testWebhookActivator{err: want}
	var boundAddress string
	listen := func(_ context.Context, network, address string) (net.Listener, error) {
		listener, err := net.Listen(network, address)
		if err == nil {
			boundAddress = listener.Addr().String()
		}
		return listener, err
	}
	if _, err := bindAndActivateServers(t.Context(), []*http.Server{{Addr: "127.0.0.1:0"}}, activation, listen); !errors.Is(err, want) {
		t.Fatalf("activation error=%v", err)
	}
	if activation.calls != 1 || activation.deactivateCalls != 1 {
		t.Fatalf("activation calls=%d deactivation calls=%d", activation.calls, activation.deactivateCalls)
	}
	rebound, err := net.Listen("tcp", boundAddress)
	if err != nil {
		t.Fatalf("listener not closed after auth failure: %v", err)
	}
	rebound.Close()
}

func TestServerFailureDeactivatesWebhookAuth(t *testing.T) {
	activation := &testWebhookActivator{}
	if err := activation.Activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := deactivateWebhook(t.Context(), activation); err != nil {
		t.Fatal(err)
	}
	if activation.calls != 1 || activation.deactivateCalls != 1 {
		t.Fatalf("activate=%d deactivate=%d", activation.calls, activation.deactivateCalls)
	}
}

func TestWebhookDeactivationRetriesBoundedly(t *testing.T) {
	activation := &testWebhookActivator{deactivateFailures: 2}
	started := time.Now()
	if err := deactivateWebhook(t.Context(), activation); err != nil {
		t.Fatal(err)
	}
	if activation.deactivateCalls != 3 {
		t.Fatalf("deactivate calls=%d", activation.deactivateCalls)
	}
	if elapsed := time.Since(started); elapsed < 200*time.Millisecond || elapsed > time.Second {
		t.Fatalf("retry duration=%s", elapsed)
	}
}

func TestWebhookStartupDeadlineClosesPreboundListeners(t *testing.T) {
	activation := &deadlineWebhookLifecycle{activate: true}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	var boundAddress string
	listen := func(_ context.Context, network, address string) (net.Listener, error) {
		listener, err := net.Listen(network, address)
		if err == nil {
			boundAddress = listener.Addr().String()
		}
		return listener, err
	}
	started := time.Now()
	_, err := bindAndActivateServers(ctx, []*http.Server{{Addr: "127.0.0.1:0"}}, activation, listen)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("startup error=%v duration=%s", err, time.Since(started))
	}
	rebound, err := net.Listen("tcp", boundAddress)
	if err != nil {
		t.Fatalf("prebound listener remained open: %v", err)
	}
	rebound.Close()
}

func TestWebhookStartupDeadlineRevokesPublishedAuthWithinTotalBudget(t *testing.T) {
	lifecycle := &publishedDeadlineWebhookLifecycle{}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	listen := func(_ context.Context, network, address string) (net.Listener, error) {
		return net.Listen(network, address)
	}
	started := time.Now()
	_, err := bindAndActivateServers(ctx, []*http.Server{{Addr: "127.0.0.1:0"}}, lifecycle, listen)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("startup error=%v duration=%s", err, time.Since(started))
	}
	if lifecycle.active || lifecycle.deactivateCalls != 1 {
		t.Fatalf("active=%t deactivate calls=%d", lifecycle.active, lifecycle.deactivateCalls)
	}
}

func TestWebhookShutdownUsesOneTotalDeadline(t *testing.T) {
	lifecycle := &deadlineWebhookLifecycle{deactivate: true, deactivateStarted: make(chan struct{})}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	serverDeadline := make(chan time.Time, 1)
	runtimeDeadline := make(chan time.Time, 1)
	started := time.Now()
	err := shutdownApplication(ctx, lifecycle, func(ctx context.Context) error {
		deadline, _ := ctx.Deadline()
		serverDeadline <- deadline
		return ctx.Err()
	}, func(ctx context.Context) error {
		deadline, _ := ctx.Deadline()
		runtimeDeadline <- deadline
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("shutdown error=%v duration=%s", err, time.Since(started))
	}
	wantDeadline, _ := ctx.Deadline()
	gotServerDeadline, gotRuntimeDeadline := <-serverDeadline, <-runtimeDeadline
	<-lifecycle.deactivateStarted
	if !gotServerDeadline.Equal(wantDeadline) || !gotRuntimeDeadline.Equal(wantDeadline) || lifecycle.deactivateCalls != 1 {
		t.Fatalf("deadlines server=%s runtime=%s want=%s revoke calls=%d", gotServerDeadline, gotRuntimeDeadline, wantDeadline, lifecycle.deactivateCalls)
	}
}

type deadlineWebhookLifecycle struct {
	activate          bool
	deactivate        bool
	deactivateCalls   int
	deactivateStarted chan struct{}
}

type publishedDeadlineWebhookLifecycle struct {
	active          bool
	deactivateCalls int
}

func (l *publishedDeadlineWebhookLifecycle) Activate(ctx context.Context) error {
	l.active = true
	<-ctx.Done()
	return ctx.Err()
}

func (l *publishedDeadlineWebhookLifecycle) Deactivate(ctx context.Context) error {
	l.deactivateCalls++
	if err := ctx.Err(); err != nil {
		return err
	}
	l.active = false
	return nil
}

func (l *deadlineWebhookLifecycle) Activate(ctx context.Context) error {
	if l.activate {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (l *deadlineWebhookLifecycle) Deactivate(ctx context.Context) error {
	l.deactivateCalls++
	if l.deactivateStarted != nil {
		close(l.deactivateStarted)
	}
	if l.deactivate {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

type testWebhookActivator struct {
	calls              int
	deactivateCalls    int
	deactivateFailures int
	err                error
}

func (a *testWebhookActivator) Deactivate(context.Context) error {
	a.deactivateCalls++
	if a.deactivateCalls <= a.deactivateFailures {
		return errors.New("temporary revoke failure")
	}
	return nil
}

func (a *testWebhookActivator) Activate(context.Context) error {
	a.calls++
	return a.err
}
