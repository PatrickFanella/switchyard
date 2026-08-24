package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNativeModelRuntimeRoutesStayOnAuthenticatedControlListener(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "secret")
	store := NewStore(nil)
	service := NewModelService(store, nil, ModelServiceOptions{DisableStart: true, QueueDepth: 4})
	api := NewAPIWithModelService(store, NewRunner(store), service)
	_, _, err := service.Admit(t.Context(), CreateModelBrokerRequestInput{ID: "queued", Model: "test", RequestBody: `{}`})
	if err != nil {
		t.Fatal(err)
	}

	ingress := httptest.NewRecorder()
	api.ModelIngressHandler().ServeHTTP(ingress, httptest.NewRequest(http.MethodGet, "/api/model-broker/queue", nil))
	if ingress.Code != http.StatusNotFound {
		t.Fatalf("model ingress status=%d", ingress.Code)
	}

	unauthenticated := httptest.NewRecorder()
	api.ControlPlaneHandler().ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/model-broker/queue", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", unauthenticated.Code)
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/api/model-broker/requests?limit=1&status=queued", nil)
	listRequest.Header.Set("Authorization", "Bearer secret")
	listResponse := httptest.NewRecorder()
	api.ControlPlaneHandler().ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), `"id":"queued"`) {
		t.Fatalf("list status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/api/model-broker/requests/queued/cancel", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	api.ControlPlaneHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Actor   string             `json:"actor"`
		Request ModelBrokerRequest `json:"request"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Actor != "token:admin" || body.Request.Status != ModelBrokerRequestCanceled {
		t.Fatalf("body=%+v", body)
	}
	stored, _ := store.ModelBrokerRequest("queued")
	if stored.CorrelationIDs["audit_actor"] != "token:admin" || stored.CorrelationIDs["audit_action"] != "cancel" || stored.CorrelationIDs["audit_reason"] != "canceled by operator" {
		t.Fatalf("audit evidence=%v", stored.CorrelationIDs)
	}
}

type failingQueueSnapshotStore struct {
	Store
}

type deadlineModelBrokerStatus struct{}

func (deadlineModelBrokerStatus) Status(ctx context.Context) ModelBrokerStatus {
	<-ctx.Done()
	return ModelBrokerStatus{Status: "unavailable", MaxDepth: 999}
}

type cancelErrorRuntime struct {
	*ModelService
	err error
}

type stalledResponseWriter struct {
	header   http.Header
	deadline <-chan time.Time
}

func (w *stalledResponseWriter) Header() http.Header { return w.header }
func (*stalledResponseWriter) WriteHeader(int)       {}
func (w *stalledResponseWriter) SetWriteDeadline(time.Time) error {
	w.deadline = time.After(20 * time.Millisecond)
	return nil
}
func (w *stalledResponseWriter) Write([]byte) (int, error) {
	if w.deadline == nil {
		select {}
	}
	<-w.deadline
	return 0, context.DeadlineExceeded
}

func TestCompatSSEStalledClientWriteIsBounded(t *testing.T) {
	w := &stalledResponseWriter{header: make(http.Header)}
	started := time.Now()
	err := writeCompatSSE(w, "status", map[string]any{"status": "running"})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(started))
	}
}

func (r cancelErrorRuntime) CancelWithEvidence(context.Context, string, ModelBrokerMutationEvidence) (ModelBrokerRequest, error) {
	return ModelBrokerRequest{}, r.err
}

func TestCompatCancelMapsRuntimeErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "not found", err: ErrNotFound, want: http.StatusNotFound},
		{name: "conflict", err: ErrConflict, want: http.StatusConflict},
		{name: "internal", err: errors.New("store failed"), want: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(nil)
			service := NewModelService(store, nil, ModelServiceOptions{DisableStart: true})
			api := NewAPIWithModelService(store, NewRunner(store), service)
			api.compatBroker.runtime = cancelErrorRuntime{ModelService: service, err: test.err}
			request := httptest.NewRequest(http.MethodDelete, "/admin/queue/request-1", nil)
			request.SetPathValue("id", "request-1")
			response := httptest.NewRecorder()
			api.llamaLineCompatQueueItem(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCompatStatusUsesIndependentQueueContextAndQueueDepth(t *testing.T) {
	store := NewStore(nil)
	service := NewModelService(store, nil, ModelServiceOptions{DisableStart: true, QueueDepth: 4})
	api := NewAPIWithModelService(store, NewRunner(store), service)
	api.modelBroker = deadlineModelBrokerStatus{}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/broker/status", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	api.ModelIngressHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		MaxDepth int `json:"max_depth"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.MaxDepth != 4 {
		t.Fatalf("body=%+v err=%v", body, err)
	}
}

func (s failingQueueSnapshotStore) WithContext(context.Context) Store { return s }

func (s failingQueueSnapshotStore) ModelBrokerQueueSnapshot(int) (ModelBrokerQueueSnapshot, error) {
	return ModelBrokerQueueSnapshot{}, errors.New("snapshot failed")
}

func TestNativeModelQueueSurfacesStoreFailure(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "secret")
	memory := NewStore(nil)
	store := failingQueueSnapshotStore{Store: memory}
	service := NewModelService(store, nil, ModelServiceOptions{DisableStart: true})
	api := NewAPIWithModelService(store, NewRunner(store), service)
	request := httptest.NewRequest(http.MethodGet, "/api/model-broker/queue", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	api.ControlPlaneHandler().ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "snapshot failed") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	compatResponse := httptest.NewRecorder()
	api.ModelIngressHandler().ServeHTTP(compatResponse, httptest.NewRequest(http.MethodGet, "/broker/status", nil))
	if compatResponse.Code != http.StatusInternalServerError || !strings.Contains(compatResponse.Body.String(), "snapshot failed") {
		t.Fatalf("compat status=%d body=%s", compatResponse.Code, compatResponse.Body.String())
	}
}

func TestNativeModelDropRejectsBodyActor(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "secret")
	store := NewStore(nil)
	service := NewModelService(store, nil, ModelServiceOptions{DisableStart: true})
	api := NewAPIWithModelService(store, NewRunner(store), service)
	_, _, _ = service.Admit(t.Context(), CreateModelBrokerRequestInput{ID: "queued", Model: "test", RequestBody: `{}`})
	request := httptest.NewRequest(http.MethodPost, "/api/model-broker/requests/queued/drop", strings.NewReader(`{"actor":"forged"}`))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	api.ControlPlaneHandler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestNativeModelMutationsPersistBoundedAuthEvidence(t *testing.T) {
	t.Setenv("SWITCHYARD_REVERSE_PROXY_AUTH", "true")
	t.Setenv("SWITCHYARD_REVERSE_PROXY_TOKEN", "model-audit-proxy-token-at-least-32-bytes")
	store := NewStore(nil)
	service := NewModelService(store, nil, ModelServiceOptions{DisableStart: true, QueueDepth: 4})
	api := NewAPIWithModelService(store, NewRunner(store), service)
	_, _, _ = service.Admit(t.Context(), CreateModelBrokerRequestInput{ID: "drop-audit", Model: "test"})
	actor := strings.Repeat("a", 256)
	reason := strings.Repeat("r", 300)
	request := httptest.NewRequest(http.MethodPost, "/api/model-broker/requests/drop-audit/drop", strings.NewReader(`{"reason":"`+reason+`"}`))
	request.Header.Set("Remote-User", actor)
	request.Header.Set("X-Switchyard-Proxy-Token", "model-audit-proxy-token-at-least-32-bytes")
	response := httptest.NewRecorder()
	api.ControlPlaneHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	stored, _ := store.ModelBrokerRequest("drop-audit")
	if len(stored.CorrelationIDs["audit_actor"]) != 256 || len(stored.CorrelationIDs["audit_reason"]) != 256 || stored.CorrelationIDs["audit_action"] != "drop" || len(stored.Error) != 256 {
		t.Fatalf("stored evidence=%v error length=%d", stored.CorrelationIDs, len(stored.Error))
	}

	_, _, _ = service.Admit(t.Context(), CreateModelBrokerRequestInput{ID: "bulk-audit", Model: "test"})
	bulk := httptest.NewRequest(http.MethodDelete, "/api/model-broker/requests", nil)
	bulk.Header.Set("Remote-User", "operator@example.test")
	bulk.Header.Set("X-Switchyard-Proxy-Token", "model-audit-proxy-token-at-least-32-bytes")
	bulkResponse := httptest.NewRecorder()
	api.ControlPlaneHandler().ServeHTTP(bulkResponse, bulk)
	if bulkResponse.Code != http.StatusOK {
		t.Fatalf("bulk status=%d body=%s", bulkResponse.Code, bulkResponse.Body.String())
	}
	stored, _ = store.ModelBrokerRequest("bulk-audit")
	if stored.CorrelationIDs["audit_actor"] != "proxy:operator@example.test" || stored.CorrelationIDs["audit_action"] != "cancel_all" || stored.CorrelationIDs["audit_reason"] != "bulk canceled by operator" {
		t.Fatalf("bulk evidence=%v", stored.CorrelationIDs)
	}
	for key, value := range stored.CorrelationIDs {
		if strings.Contains(key, "model-audit-proxy-token") || strings.Contains(value, "model-audit-proxy-token") {
			t.Fatal("proxy token persisted in audit evidence")
		}
	}

	_, _, _ = service.Begin(t.Context(), CreateModelBrokerRequestInput{ID: "first-audit", Model: "test"})
	first, err := service.CancelWithEvidence(t.Context(), "first-audit", ModelBrokerMutationEvidence{Actor: "first", Reason: "first reason"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := service.CancelAllWithEvidence(t.Context(), ModelBrokerMutationEvidence{Actor: "second", Reason: "second reason"})
	if err != nil || len(rows) != 0 {
		t.Fatalf("recancel rows=%d err=%v", len(rows), err)
	}
	stored, _ = store.ModelBrokerRequest("first-audit")
	if stored.CorrelationIDs["audit_actor"] != "first" || stored.CorrelationIDs["audit_reason"] != "first reason" || stored.CorrelationIDs["audit_action"] != "cancel" || !stored.CancelRequestedAt.Equal(*first.CancelRequestedAt) {
		t.Fatalf("first audit overwritten: %+v", stored)
	}
}
