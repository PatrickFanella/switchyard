package controlplane

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const webhookTestSecret = "0123456789abcdef0123456789abcdef"

var webhookTestNow = time.Unix(1787572800, 0).UTC()
var webhookTestBody = []byte(`{"repo":{"name":"switchyard"},"count":2,"ok":true}`)

func TestWebhookCanonicalSignatureVector(t *testing.T) {
	digest := sha256.Sum256(webhookTestBody)
	if got := hex.EncodeToString(digest[:]); got != "c34e3c1725083aa68b1495b0e3c12d3cd0634c42c66402768091be635ea12f8a" {
		t.Fatalf("body digest = %s", got)
	}
	canonical := WebhookCanonicalString("hook-1", "delivery-1", "1787572800", hex.EncodeToString(digest[:]))
	mac := hmac.New(sha256.New, []byte(webhookTestSecret))
	_, _ = mac.Write([]byte(canonical))
	if got := hex.EncodeToString(mac.Sum(nil)); got != "f90b7cc85dbbd5ce86d39765eac58c90cb7428d50a78bea0a85b14c01589d11a" {
		t.Fatalf("signature = %s", got)
	}
}

func TestWebhookDispatchAdmissionReplayAndAuditRedaction(t *testing.T) {
	store, dispatcher := webhookTestDispatcher(t, nil)
	response := dispatchWebhook(dispatcher.Handler(), "delivery-1", webhookTestBody, nil)
	if response.Code != http.StatusAccepted {
		t.Fatalf("first status = %d body=%s", response.Code, response.Body.String())
	}
	var accepted struct {
		RunID    string `json:"run_id"`
		Replayed bool   `json:"replayed"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	detail, err := store.RunDetail(accepted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Replayed || detail.Run.Status != RunQueued || detail.Run.Inputs["repo"] != "switchyard" || detail.Run.Inputs["count"] != "2" || detail.Run.PipelineSnapshot == nil {
		t.Fatalf("admission=%+v run=%+v", accepted, detail.Run)
	}
	if len(detail.Events) != 2 || detail.Events[0].Type != "run.created" || detail.Events[1].Type != "webhook.accepted" {
		t.Fatalf("events = %+v", detail.Events)
	}
	persisted, _ := json.Marshal(detail)
	for _, forbidden := range []string{string(webhookTestBody), webhookTestSecret, "v1=", "hook.key"} {
		if bytes.Contains(persisted, []byte(forbidden)) {
			t.Fatalf("persisted webhook material %q: %s", forbidden, persisted)
		}
	}
	replay := dispatchWebhook(dispatcher.Handler(), "delivery-1", webhookTestBody, nil)
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), accepted.RunID) || !strings.Contains(replay.Body.String(), `"replayed":true`) || len(store.ListRuns()) != 1 {
		t.Fatalf("replay status=%d body=%s runs=%d", replay.Code, replay.Body.String(), len(store.ListRuns()))
	}
	changed := dispatchWebhook(dispatcher.Handler(), "delivery-1", []byte(`{"repo":{"name":"other"},"count":2,"ok":true}`), nil)
	if changed.Code != http.StatusConflict || len(store.ListRuns()) != 1 {
		t.Fatalf("changed replay status=%d body=%s", changed.Code, changed.Body.String())
	}
	claimed, err := store.ClaimNextRun("worker", time.Minute)
	if err != nil || claimed.ID != accepted.RunID {
		t.Fatalf("worker claim=%+v err=%v", claimed, err)
	}
	finished, err := NewRunner(store).ExecuteClaimedRun(t.Context(), claimed, "worker")
	if err != nil || finished.Status != RunSucceeded {
		t.Fatalf("worker execution=%+v err=%v", finished, err)
	}
	if _, err := store.PruneRetention(webhookTestNow.Add(31 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	retainedReplay := dispatchWebhook(dispatcher.Handler(), "delivery-1", webhookTestBody, nil)
	if retainedReplay.Code != http.StatusOK || !strings.Contains(retainedReplay.Body.String(), accepted.RunID) {
		t.Fatalf("post-retention replay status=%d body=%s", retainedReplay.Code, retainedReplay.Body.String())
	}
}

func TestWebhookDispatchStrictRequestBoundary(t *testing.T) {
	_, dispatcher := webhookTestDispatcher(t, nil)
	tests := []struct {
		name   string
		body   []byte
		mutate func(*http.Request)
		want   int
	}{
		{name: "duplicate delivery", body: webhookTestBody, mutate: func(r *http.Request) { r.Header.Add(WebhookDeliveryHeader, "other") }, want: http.StatusUnauthorized},
		{name: "duplicate timestamp", body: webhookTestBody, mutate: func(r *http.Request) { r.Header.Add(WebhookTimestampHeader, "1787572800") }, want: http.StatusUnauthorized},
		{name: "duplicate key", body: webhookTestBody, mutate: func(r *http.Request) { r.Header.Add(WebhookKeyIDHeader, "key-1") }, want: http.StatusUnauthorized},
		{name: "duplicate signature", body: webhookTestBody, mutate: func(r *http.Request) { r.Header.Add(WebhookSignatureHeader, r.Header.Get(WebhookSignatureHeader)) }, want: http.StatusUnauthorized},
		{name: "old timestamp", body: webhookTestBody, mutate: func(r *http.Request) {
			signWebhookRequest(r, "delivery-old", webhookTestNow.Add(-5*time.Minute-time.Second))
		}, want: http.StatusUnauthorized},
		{name: "future timestamp", body: webhookTestBody, mutate: func(r *http.Request) {
			signWebhookRequest(r, "delivery-future", webhookTestNow.Add(5*time.Minute+time.Second))
		}, want: http.StatusUnauthorized},
		{name: "timestamp duration overflow", body: webhookTestBody, mutate: func(r *http.Request) {
			signWebhookRequest(r, "delivery-overflow", time.Unix(99999999999, 0))
		}, want: http.StatusUnauthorized},
		{name: "content type parameter", body: webhookTestBody, mutate: func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=utf-8") }, want: http.StatusUnsupportedMediaType},
		{name: "content encoding", body: webhookTestBody, mutate: func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }, want: http.StatusUnsupportedMediaType},
		{name: "invalid json", body: []byte(`{"repo":`), want: http.StatusBadRequest},
		{name: "duplicate json key", body: []byte(`{"repo":{"name":"first","name":"second"},"count":2,"ok":true}`), want: http.StatusBadRequest},
		{name: "body cap", body: bytes.Repeat([]byte("x"), int(webhookMaxBodyBytes)+1), want: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := dispatchWebhook(dispatcher.Handler(), "delivery-"+strings.ReplaceAll(test.name, " ", "-"), test.body, test.mutate)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestWebhookUnauthenticatedResponsesDoNotEnumerateTriggers(t *testing.T) {
	_, dispatcher := webhookTestDispatcher(t, nil)
	request := httptest.NewRequest(http.MethodPost, "/hooks/v1/hook-1", bytes.NewReader(webhookTestBody))
	request.Header.Set("Content-Type", "application/json")
	signWebhookRequest(request, "enumeration", webhookTestNow)
	request.Header.Set(WebhookSignatureHeader, "v1="+strings.Repeat("0", 64))
	unknown := request.Clone(request.Context())
	unknown.URL.Path = "/hooks/v1/unknown-hook"
	unknown.SetPathValue("triggerID", "unknown-hook")
	unknown.Body = io.NopCloser(bytes.NewReader(webhookTestBody))
	knownResponse, unknownResponse := httptest.NewRecorder(), httptest.NewRecorder()
	dispatcher.Handler().ServeHTTP(knownResponse, request)
	dispatcher.Handler().ServeHTTP(unknownResponse, unknown)
	if knownResponse.Code != http.StatusUnauthorized || unknownResponse.Code != knownResponse.Code || unknownResponse.Body.String() != knownResponse.Body.String() || unknownResponse.Header().Get("WWW-Authenticate") != knownResponse.Header().Get("WWW-Authenticate") {
		t.Fatalf("known=%d %q unknown=%d %q", knownResponse.Code, knownResponse.Body.String(), unknownResponse.Code, unknownResponse.Body.String())
	}
}

type gatedWebhookBody struct {
	started chan struct{}
	release chan struct{}
}

func (b *gatedWebhookBody) Read([]byte) (int, error) {
	select {
	case <-b.started:
	default:
		close(b.started)
	}
	<-b.release
	return 0, io.EOF
}

func (*gatedWebhookBody) Close() error { return nil }

func TestWebhookSlowPreAuthBodyDoesNotConsumeAdmissionSlot(t *testing.T) {
	_, dispatcher := webhookTestDispatcher(t, nil)
	body := &gatedWebhookBody{started: make(chan struct{}), release: make(chan struct{})}
	request := httptest.NewRequest(http.MethodPost, "/hooks/v1/hook-1", nil)
	request.Body, request.ContentLength = body, -1
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		dispatcher.Handler().ServeHTTP(response, request)
		close(done)
	}()
	<-body.started
	if len(dispatcher.semaphore) != 0 {
		t.Fatal("slow unauthenticated body consumed admission semaphore")
	}
	if got := dispatchWebhook(dispatcher.Handler(), "while-slow", webhookTestBody, nil).Code; got != http.StatusAccepted {
		t.Fatalf("authenticated request status=%d", got)
	}
	close(body.release)
	<-done
}

func TestWebhookSlowBodyIsBoundedByAdmissionDeadlineOnServer(t *testing.T) {
	store := webhookTestStore(t)
	paths := writeWebhookConfig(t, webhookConfigJSON("hook-1", "revision-1", false))
	dispatcher, err := PrepareWebhookDispatcher(paths.config, paths.dir, store, WebhookDispatcherOptions{Now: func() time.Time { return webhookTestNow }, Timeout: 100 * time.Millisecond, MaxConcurrency: 1, RatePerSecond: 100, RunRetention: 30 * 24 * time.Hour, Owner: "test-deployment"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(dispatcher.Handler())
	t.Cleanup(server.Close)
	connection, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(connection, "POST /hooks/v1/hook-1 HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 1\r\nConnection: close\r\n\r\n", server.Listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || time.Since(started) > time.Second {
		t.Fatalf("status=%d duration=%s", response.StatusCode, time.Since(started))
	}
}

func TestWebhookListenerIsolationAllMethods(t *testing.T) {
	_, dispatcher := webhookTestDispatcher(t, nil)
	handler := dispatcher.Handler()
	for _, path := range []string{"/api/runs", "/health", "/readyz", "/metrics", "/v1/models", "/admin/audit", "/hooks/v1/hook-1/extra"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
			if response.Code != http.StatusNotFound {
				t.Errorf("%s %s status=%d", method, path, response.Code)
			}
		}
	}
	for _, handler := range []http.Handler{NewAPI(NewStore(nil), NewRunner(NewStore(nil))).ControlPlaneHandler(), NewAPI(NewStore(nil), NewRunner(NewStore(nil))).ModelIngressHandler()} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, "/hooks/v1/hook-1", nil))
			if response.Code != http.StatusNotFound {
				t.Errorf("non-webhook listener %s status=%d", method, response.Code)
			}
		}
	}
}

func TestWebhookMemoryAdmissionConcurrentArbitrationAndRevisionRace(t *testing.T) {
	store, dispatcher := webhookTestDispatcher(t, nil)
	const callers = 24
	var wg sync.WaitGroup
	runIDs := make(chan string, callers)
	statuses := make(chan int, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := dispatchWebhook(dispatcher.Handler(), "same-delivery", webhookTestBody, nil)
			statuses <- response.Code
			var body map[string]any
			_ = json.Unmarshal(response.Body.Bytes(), &body)
			if id, ok := body["run_id"].(string); ok {
				runIDs <- id
			}
		}()
	}
	wg.Wait()
	close(runIDs)
	close(statuses)
	unique := map[string]bool{}
	for id := range runIDs {
		unique[id] = true
	}
	for status := range statuses {
		if status != http.StatusAccepted && status != http.StatusOK {
			t.Errorf("concurrent status=%d", status)
		}
	}
	if len(unique) != 1 || len(store.ListRuns()) != 1 {
		t.Fatalf("unique runs=%v stored=%d", unique, len(store.ListRuns()))
	}
	if err := store.ReplaceWebhookAuth(t.Context(), webhookAuthTestConfig(2, "revision-1")); err != nil {
		t.Fatal(err)
	}
	response := dispatchWebhook(dispatcher.Handler(), "revision-race", webhookTestBody, nil)
	if response.Code != http.StatusConflict || len(store.ListRuns()) != 1 {
		t.Fatalf("revision race status=%d runs=%d", response.Code, len(store.ListRuns()))
	}
}

func TestWebhookInactiveKeysFailBeforeAdmission(t *testing.T) {
	for _, test := range []struct {
		name      string
		notBefore time.Time
		notAfter  time.Time
	}{
		{name: "not yet valid", notBefore: webhookTestNow.Add(time.Minute), notAfter: webhookTestNow.Add(time.Hour)},
		{name: "expired", notBefore: webhookTestNow.Add(-time.Hour), notAfter: webhookTestNow},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, dispatcher := webhookTestDispatcher(t, nil)
			snapshot := dispatcher.snapshots["hook-1"]
			key := snapshot.keys["key-1"]
			key.notBefore, key.notAfter = test.notBefore, test.notAfter
			snapshot.keys["key-1"] = key
			dispatcher.snapshots["hook-1"] = snapshot
			response := dispatchWebhook(dispatcher.Handler(), "inactive-key", webhookTestBody, nil)
			if response.Code != http.StatusUnauthorized || len(dispatcher.semaphore) != 0 || len(store.ListRuns()) != 0 {
				t.Fatalf("status=%d semaphore=%d runs=%d", response.Code, len(dispatcher.semaphore), len(store.ListRuns()))
			}
		})
	}
}

func TestWebhookRateAndConcurrencyBounds(t *testing.T) {
	store := webhookTestStore(t)
	paths := writeWebhookConfig(t, webhookConfigJSON("hook-1", "revision-1", false))
	dispatcher, err := PrepareWebhookDispatcher(paths.config, paths.dir, store, WebhookDispatcherOptions{Now: func() time.Time { return webhookTestNow }, RatePerSecond: 1, MaxConcurrency: 1, RunRetention: 30 * 24 * time.Hour, Owner: "test-deployment"})
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := dispatchWebhook(dispatcher.Handler(), "rate-1", webhookTestBody, nil).Code; got != http.StatusAccepted {
		t.Fatalf("first rate status=%d", got)
	}
	if got := dispatchWebhook(dispatcher.Handler(), "rate-2", webhookTestBody, nil).Code; got != http.StatusTooManyRequests {
		t.Fatalf("rate status=%d", got)
	}
	request := httptest.NewRequest(http.MethodPost, "/hooks/v1/hook-1", bytes.NewReader(webhookTestBody))
	request.Header.Set("X-Forwarded-For", "203.0.113.99")
	request.Header.Set("Content-Type", "application/json")
	signWebhookRequest(request, "spoofed-forwarded", webhookTestNow)
	responseRecorder := httptest.NewRecorder()
	dispatcher.Handler().ServeHTTP(responseRecorder, request)
	if responseRecorder.Code != http.StatusTooManyRequests {
		t.Fatalf("forwarded address bypass status=%d", responseRecorder.Code)
	}

	store = webhookTestStore(t)
	paths = writeWebhookConfig(t, webhookConfigJSON("hook-1", "revision-1", false))
	dispatcher, err = PrepareWebhookDispatcher(paths.config, paths.dir, store, WebhookDispatcherOptions{Now: func() time.Time { return webhookTestNow }, RatePerSecond: 100, MaxConcurrency: 1, RunRetention: 30 * 24 * time.Hour, Owner: "test-deployment"})
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	dispatcher.semaphore <- struct{}{}
	response := dispatchWebhook(dispatcher.Handler(), "concurrency", webhookTestBody, nil)
	<-dispatcher.semaphore
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("concurrency status=%d", response.Code)
	}
}

func TestWebhookConfigReplacementIsAtomic(t *testing.T) {
	store, dispatcher := webhookTestDispatcher(t, nil)
	var config, missing WebhookConfig
	if err := json.Unmarshal([]byte(webhookConfigJSON("hook-1", "revision-2", false)), &config); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(webhookConfigJSON("missing-hook", "revision-2", false)), &missing); err != nil {
		t.Fatal(err)
	}
	config.Triggers = append(config.Triggers, missing.Triggers[0])
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	paths := writeWebhookConfig(t, string(encoded))
	if _, err := PrepareWebhookDispatcher(paths.config, paths.dir, store, webhookTestOptions()); err == nil {
		t.Fatal("expected invalid second trigger rejection")
	}
	if store.webhookAuthGeneration != 1 {
		t.Fatalf("failed preparation changed active generation to %d", store.webhookAuthGeneration)
	}
	response := dispatchWebhook(dispatcher.Handler(), "after-failed-reload", webhookTestBody, nil)
	if response.Code != http.StatusAccepted {
		t.Fatalf("failed reload changed active revision: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWebhookConfigPermissionsRotationAndProjectionValidation(t *testing.T) {
	t.Run("secret must be read only", func(t *testing.T) {
		store := webhookTestStore(t)
		paths := writeWebhookConfig(t, webhookConfigJSON("hook-1", "revision-1", false))
		if err := os.Chmod(paths.secret, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareWebhookDispatcher(paths.config, paths.dir, store, webhookTestOptions()); err == nil {
			t.Fatal("expected writable secret rejection")
		}
	})
	t.Run("secret directory must be private", func(t *testing.T) {
		store := webhookTestStore(t)
		paths := writeWebhookConfig(t, webhookConfigJSON("hook-1", "revision-1", false))
		if err := os.Chmod(paths.dir, 0750); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareWebhookDispatcher(paths.config, paths.dir, store, webhookTestOptions()); err == nil {
			t.Fatal("expected non-private directory rejection")
		}
	})
	t.Run("secret symlink rejected", func(t *testing.T) {
		store := webhookTestStore(t)
		paths := writeWebhookConfig(t, webhookConfigJSON("hook-1", "revision-1", false))
		target := filepath.Join(paths.dir, "target.key")
		if err := os.WriteFile(target, []byte(webhookTestSecret), 0400); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(paths.secret); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, paths.secret); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareWebhookDispatcher(paths.config, paths.dir, store, webhookTestOptions()); err == nil {
			t.Fatal("expected symlink rejection")
		}
	})
	t.Run("rotation overlap bounded", func(t *testing.T) {
		store := webhookTestStore(t)
		paths := writeWebhookConfig(t, webhookConfigJSON("hook-1", "revision-1", true))
		if _, err := PrepareWebhookDispatcher(paths.config, paths.dir, store, webhookTestOptions()); err == nil {
			t.Fatal("expected excessive rotation overlap rejection")
		}
	})
	t.Run("bounded rotation overlap accepted", func(t *testing.T) {
		store := webhookTestStore(t)
		config := webhookConfigJSON("hook-1", "revision-1", false)
		second := fmt.Sprintf(`,{"id":"key-2","secret_file":"hook.key","not_before":%q,"not_after":%q}`, webhookTestNow.Add(-30*time.Minute).Format(time.RFC3339), webhookTestNow.Add(30*time.Minute).Format(time.RFC3339))
		config = strings.Replace(config, `}],"projections"`, `}`+second+`],"projections"`, 1)
		paths := writeWebhookConfig(t, config)
		if _, err := PrepareWebhookDispatcher(paths.config, paths.dir, store, webhookTestOptions()); err != nil {
			t.Fatalf("bounded rotation rejected: %v config=%s", err, config)
		}
	})
	t.Run("sensitive projection name rejected", func(t *testing.T) {
		store := webhookTestStore(t)
		config := strings.Replace(webhookConfigJSON("hook-1", "revision-1", false), `"repo":{"pointer":"/repo/name"}`, `"api_token":{"pointer":"/repo/name"}`, 1)
		paths := writeWebhookConfig(t, config)
		if _, err := PrepareWebhookDispatcher(paths.config, paths.dir, store, webhookTestOptions()); err == nil {
			t.Fatal("expected sensitive projection rejection")
		}
		if store.webhookAuthGeneration != 0 || len(store.webhookAuth) != 0 {
			t.Fatalf("failed config preparation published generation=%d auth=%d", store.webhookAuthGeneration, len(store.webhookAuth))
		}
	})
	t.Run("sensitive source alias rejected", func(t *testing.T) {
		store := webhookTestStore(t)
		config := strings.Replace(webhookConfigJSON("hook-1", "revision-1", false), `"pointer":"/repo/name"`, `"pointer":"/payload/api~1token"`, 1)
		paths := writeWebhookConfig(t, config)
		if _, err := PrepareWebhookDispatcher(paths.config, paths.dir, store, webhookTestOptions()); err == nil {
			t.Fatal("expected sensitive decoded pointer segment rejection")
		}
	})
	t.Run("projection count capped", func(t *testing.T) {
		store := webhookTestStore(t)
		var config WebhookConfig
		if err := json.Unmarshal([]byte(webhookConfigJSON("hook-1", "revision-1", false)), &config); err != nil {
			t.Fatal(err)
		}
		config.Triggers[0].Projections = map[string]WebhookProjection{}
		for index := range webhookMaxProjections + 1 {
			config.Triggers[0].Projections[fmt.Sprintf("input-%d", index)] = WebhookProjection{Pointer: "/count"}
		}
		encoded, _ := json.Marshal(config)
		paths := writeWebhookConfig(t, string(encoded))
		if _, err := PrepareWebhookDispatcher(paths.config, paths.dir, store, webhookTestOptions()); err == nil {
			t.Fatal("expected projection count rejection")
		}
	})
}

func TestWebhookProjectionSizeAndAmplificationBounds(t *testing.T) {
	tooLarge := []byte(`{"value":"` + strings.Repeat("x", webhookMaxScalarBytes+1) + `"}`)
	if _, _, err := projectWebhookInputs(tooLarge, map[string]WebhookProjection{"value": {Pointer: "/value"}}); err == nil {
		t.Fatal("expected scalar size rejection")
	}
	value := strings.Repeat("x", 4000)
	body, _ := json.Marshal(map[string]string{"value": value})
	projections := map[string]WebhookProjection{}
	for index := range 5 {
		projections[fmt.Sprintf("copy-%d", index)] = WebhookProjection{Pointer: "/value"}
	}
	if _, _, err := projectWebhookInputs(body, projections); err == nil {
		t.Fatal("expected total projected input size rejection")
	}
	inputs, types := map[string]string{}, map[string]string{}
	for index := range webhookMaxProjections + 1 {
		key := fmt.Sprintf("input-%d", index)
		inputs[key], types[key] = "x", "string"
	}
	if err := validateWebhookAdmissionInput(AdmitWebhookInput{TriggerID: "hook", DeliveryID: "delivery", BodySHA256: strings.Repeat("a", 64), AuthGeneration: 1, AuthRevision: "revision", KeyID: "key", Inputs: inputs, InputTypes: types}); err == nil {
		t.Fatal("store boundary accepted projection amplification")
	}
}

func TestWebhookConfigFencingMemory(t *testing.T) {
	store := webhookTestStore(t)
	first := webhookAuthTestConfig(1, "revision-1")
	if err := store.ReplaceWebhookAuth(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceWebhookAuth(t.Context(), first); err != nil {
		t.Fatalf("idempotent replacement: %v", err)
	}
	disjoint := first
	disjoint.Digest = strings.Repeat("f", 64)
	if err := store.ReplaceWebhookAuth(t.Context(), disjoint); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-generation disjoint error=%v", err)
	}
	newer := webhookAuthTestConfig(2, "revision-2")
	if err := store.ReplaceWebhookAuth(t.Context(), newer); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceWebhookAuth(t.Context(), first); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale generation error=%v", err)
	}
	foreign := webhookAuthTestConfig(3, "revision-3")
	foreign.Owner = "other-deployment"
	if err := store.ReplaceWebhookAuth(t.Context(), foreign); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign owner error=%v", err)
	}
}

func TestWebhookDeactivationFencesDispatcherAndAllowsSameConfigRestart(t *testing.T) {
	store, dispatcher := webhookTestDispatcher(t, nil)
	if err := dispatcher.Deactivate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if store.webhookAuthGeneration != 1 || len(store.webhookAuth) != 0 {
		t.Fatalf("deactivated generation=%d auth=%d", store.webhookAuthGeneration, len(store.webhookAuth))
	}
	if response := dispatchWebhook(dispatcher.Handler(), "after-stop", webhookTestBody, nil); response.Code != http.StatusConflict {
		t.Fatalf("stale dispatcher status=%d body=%s", response.Code, response.Body.String())
	}
	if err := dispatcher.Activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if response := dispatchWebhook(dispatcher.Handler(), "after-restart", webhookTestBody, nil); response.Code != http.StatusAccepted {
		t.Fatalf("restarted dispatcher status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWebhookLifecycleCanceledContextDoesNotPublishMemoryAuth(t *testing.T) {
	store := webhookTestStore(t)
	paths := writeWebhookConfig(t, webhookConfigJSON("hook-1", "revision-1", false))
	dispatcher, err := PrepareWebhookDispatcher(paths.config, paths.dir, store, webhookTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := dispatcher.Activate(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("activation error=%v", err)
	}
	if store.webhookAuthGeneration != 0 || len(store.webhookAuth) != 0 {
		t.Fatalf("canceled activation published generation=%d auth=%d", store.webhookAuthGeneration, len(store.webhookAuth))
	}
}

func TestWebhookMemoryLifecycleLockWaitHonorsContext(t *testing.T) {
	store := webhookTestStore(t)
	store.mu.Lock()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- store.ReplaceWebhookAuth(ctx, webhookAuthTestConfig(1, "revision-1")) }()
	err := <-done
	store.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("activation error=%v", err)
	}
}

func TestWebhookSameGenerationSecretChangeIsFenced(t *testing.T) {
	store := webhookTestStore(t)
	paths := writeWebhookConfig(t, webhookConfigJSON("hook-1", "revision-1", false))
	first, err := PrepareWebhookDispatcher(paths.config, paths.dir, store, webhookTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(paths.secret, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.secret, []byte(strings.Repeat("z", 32)), 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(paths.secret, 0400); err != nil {
		t.Fatal(err)
	}
	replacement, err := PrepareWebhookDispatcher(paths.config, paths.dir, store, webhookTestOptions())
	if err == nil {
		err = replacement.Activate(t.Context())
	}
	if !errors.Is(err, ErrConflict) && !strings.Contains(fmt.Sprint(err), ErrConflict.Error()) {
		t.Fatalf("same-generation secret replacement error=%v", err)
	}
	if store.webhookAuthGeneration != 1 || store.webhookAuth["hook-1"].Revision != "revision-1" {
		t.Fatalf("failed auth replacement changed active generation=%d auth=%+v", store.webhookAuthGeneration, store.webhookAuth["hook-1"])
	}
}

func TestWebhookReceiptExpiryPruneReplayAndRunAttempts(t *testing.T) {
	now := webhookTestNow
	store := webhookTestStore(t)
	store.now = func() time.Time { return now }
	config := webhookAuthTestConfig(1, "revision-1")
	config.ReceiptRetention = 24 * time.Hour
	config.Triggers[0].Keys[0].NotAfter = webhookTestNow.Add(7 * 24 * time.Hour)
	if err := store.ReplaceWebhookAuth(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWITCHYARD_RUN_MAX_ATTEMPTS", "7")
	input := AdmitWebhookInput{TriggerID: "hook-1", DeliveryID: "expiring", BodySHA256: strings.Repeat("a", 64), AuthGeneration: 1, AuthRevision: "revision-1", KeyID: "key-1", Inputs: map[string]string{"repo": "switchyard", "count": "2", "ok": "true"}, InputTypes: map[string]string{"repo": "string", "count": "number", "ok": "boolean"}}
	first, err := store.AdmitWebhook(input)
	if err != nil || first.Run.MaxAttempts != 7 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	replay, err := store.AdmitWebhook(input)
	if err != nil || !replay.Replayed || replay.Run.ID != first.Run.ID {
		t.Fatalf("pre-expiry replay=%+v err=%v", replay, err)
	}
	now = now.Add(24 * time.Hour)
	fresh, err := store.AdmitWebhook(input)
	if err != nil || fresh.Replayed || fresh.Run.ID == first.Run.ID {
		t.Fatalf("post-expiry admission=%+v err=%v", fresh, err)
	}
	input.DeliveryID = "pruned"
	if _, err := store.AdmitWebhook(input); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	result, err := store.PruneRetention(time.Time{})
	if err != nil || result.WebhookReceipts != 2 {
		t.Fatalf("prune=%+v err=%v", result, err)
	}
	metrics, _ := store.RuntimeMetrics()
	if metrics.WebhookReceiptsRetained != 0 || metrics.WebhookReceiptsPruned != 3 {
		t.Fatalf("receipt metrics=%+v", metrics)
	}
}

func TestWebhookMemoryReceiptPruneIsBatched(t *testing.T) {
	now := webhookTestNow
	store := NewStore(func() time.Time { return now })
	store.webhookReceiptRetention = 24 * time.Hour
	for index := range retentionBatchSize + 1 {
		store.webhookReceipts[fmt.Sprint(index)] = webhookReceipt{acceptedAt: now.Add(-24 * time.Hour)}
	}
	first, err := store.PruneRetention(time.Time{})
	if err != nil || first.WebhookReceipts != retentionBatchSize || len(store.webhookReceipts) != 1 {
		t.Fatalf("first prune=%+v retained=%d err=%v", first, len(store.webhookReceipts), err)
	}
	second, err := store.PruneRetention(time.Time{})
	if err != nil || second.WebhookReceipts != 1 || len(store.webhookReceipts) != 0 {
		t.Fatalf("second prune=%+v retained=%d err=%v", second, len(store.webhookReceipts), err)
	}
}

func TestWebhookMemoryAdmissionHonorsContextDuringLockWait(t *testing.T) {
	store := webhookTestStore(t)
	if err := store.ReplaceWebhookAuth(t.Context(), webhookAuthTestConfig(1, "revision-1")); err != nil {
		t.Fatal(err)
	}
	input := AdmitWebhookInput{TriggerID: "hook-1", DeliveryID: "canceled", BodySHA256: strings.Repeat("a", 64), AuthGeneration: 1, AuthRevision: "revision-1", KeyID: "key-1", Inputs: map[string]string{"repo": "switchyard", "count": "2", "ok": "true"}, InputTypes: map[string]string{"repo": "string", "count": "number", "ok": "boolean"}}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	store.mu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := store.AdmitWebhookContext(ctx, input)
		done <- err
	}()
	err := <-done
	store.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("admission error=%v", err)
	}
	if len(store.runs) != 0 || len(store.webhookReceipts) != 0 {
		t.Fatalf("canceled admission mutated runs=%d receipts=%d", len(store.runs), len(store.webhookReceipts))
	}
}

func TestWebhookOpenatDirectorySwapKeepsOpenedDirectory(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "secrets")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hook.key"), []byte(webhookTestSecret), 0400); err != nil {
		t.Fatal(err)
	}
	fd, err := openPrivateDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	original := filepath.Join(parent, "original")
	attacker := filepath.Join(parent, "attacker")
	if err := os.Rename(dir, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(attacker, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attacker, "hook.key"), []byte(strings.Repeat("z", 32)), 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(attacker, dir); err != nil {
		t.Fatal(err)
	}
	secret, err := readPrivateFileAt(fd, "hook.key", true, 4096)
	if err != nil || string(secret) != webhookTestSecret {
		t.Fatalf("secret=%q err=%v", secret, err)
	}
}

type webhookPaths struct{ dir, config, secret string }

func webhookTestDispatcher(t *testing.T, mutate func(string) string) (*MemoryStore, *WebhookDispatcher) {
	t.Helper()
	store := webhookTestStore(t)
	config := webhookConfigJSON("hook-1", "revision-1", false)
	if mutate != nil {
		config = mutate(config)
	}
	paths := writeWebhookConfig(t, config)
	dispatcher, err := PrepareWebhookDispatcher(paths.config, paths.dir, store, WebhookDispatcherOptions{Now: func() time.Time { return webhookTestNow }, RatePerSecond: 1000, RunRetention: 30 * 24 * time.Hour, Owner: "test-deployment"})
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return store, dispatcher
}

func webhookTestStore(t *testing.T) *MemoryStore {
	t.Helper()
	store := NewStore(func() time.Time { return webhookTestNow })
	if err := store.UpsertWorkflow(Workflow{ID: "webhook-workflow", Kind: WorkflowChangemaker, Name: "Webhook workflow", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	_, err := store.UpsertWorkflowDefinition(WorkflowDefinition{ID: "webhook-workflow", Name: "Webhook workflow", Status: "active", InputShape: JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"static": {Type: "string"}, "repo": {Type: "string"}, "count": {Type: "number"}, "ok": {Type: "boolean"}}, Required: []string{"static", "repo", "count", "ok"}}, Pipeline: PipelineDefinition{ID: "webhook-pipeline", WorkflowID: "webhook-workflow", Name: "Webhook pipeline", Steps: []PipelineStepDefinition{{ID: "prepare", Position: 1, Name: "webhook.prepare", Executor: "internal"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTrigger(Trigger{ID: "hook-1", WorkflowID: "webhook-workflow", Type: TriggerWebhook, Name: "Hook", Enabled: true, Inputs: map[string]string{"static": "configured"}}); err != nil {
		t.Fatal(err)
	}
	return store
}

func webhookConfigJSON(trigger, revision string, excessiveOverlap bool) string {
	firstNotAfter := webhookTestNow.Add(time.Hour)
	if excessiveOverlap {
		firstNotAfter = webhookTestNow.Add(48 * time.Hour)
	}
	keys := fmt.Sprintf(`{"id":"key-1","secret_file":"hook.key","not_before":%q,"not_after":%q}`, webhookTestNow.Add(-time.Hour).Format(time.RFC3339), firstNotAfter.Format(time.RFC3339))
	if excessiveOverlap {
		keys += fmt.Sprintf(`,{"id":"key-2","secret_file":"hook.key","not_before":%q,"not_after":%q}`, webhookTestNow.Add(-time.Hour).Format(time.RFC3339), webhookTestNow.Add(48*time.Hour).Format(time.RFC3339))
	}
	return fmt.Sprintf(`{"version":1,"owner":"test-deployment","generation":1,"receipt_retention":"720h","triggers":[{"trigger_id":%q,"revision":%q,"keys":[%s],"projections":{"repo":{"pointer":"/repo/name"},"count":{"pointer":"/count"},"ok":{"pointer":"/ok"}}}]}`, trigger, revision, keys)
}

func webhookTestOptions() WebhookDispatcherOptions {
	return WebhookDispatcherOptions{Now: func() time.Time { return webhookTestNow }, RunRetention: 30 * 24 * time.Hour, Owner: "test-deployment"}
}

func webhookAuthTestConfig(generation int64, revision string) WebhookAuthConfig {
	return WebhookAuthConfig{Owner: "test-deployment", Generation: generation, Digest: fmt.Sprintf("%064x", generation), ReceiptRetention: 30 * 24 * time.Hour, Triggers: []WebhookAuthMetadata{{TriggerID: "hook-1", Revision: revision, Keys: []WebhookAuthKey{{ID: "key-1", NotBefore: webhookTestNow.Add(-time.Hour), NotAfter: webhookTestNow.Add(time.Hour)}}}}}
}

func writeWebhookConfig(t *testing.T, config string) webhookPaths {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "webhooks.json")
	secretPath := filepath.Join(dir, "hook.key")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretPath, []byte(webhookTestSecret), 0400); err != nil {
		t.Fatal(err)
	}
	return webhookPaths{dir: dir, config: configPath, secret: secretPath}
}

func dispatchWebhook(handler http.Handler, delivery string, body []byte, mutate func(*http.Request)) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/hooks/v1/hook-1", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	signWebhookRequest(request, delivery, webhookTestNow)
	if mutate != nil {
		mutate(request)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func signWebhookRequest(request *http.Request, delivery string, at time.Time) {
	body, _ := io.ReadAll(request.Body)
	request.Body = io.NopCloser(bytes.NewReader(body))
	digest := sha256.Sum256(body)
	timestamp := fmt.Sprint(at.Unix())
	mac := hmac.New(sha256.New, []byte(webhookTestSecret))
	_, _ = mac.Write([]byte(WebhookCanonicalString("hook-1", delivery, timestamp, hex.EncodeToString(digest[:]))))
	request.Header.Set(WebhookDeliveryHeader, delivery)
	request.Header.Set(WebhookTimestampHeader, timestamp)
	request.Header.Set(WebhookKeyIDHeader, "key-1")
	request.Header.Set(WebhookSignatureHeader, "v1="+hex.EncodeToString(mac.Sum(nil)))
}
