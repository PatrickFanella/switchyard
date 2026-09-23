package controlplane

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"
)

type OpenPilotRepositoryStatusProvider interface {
	Status(context.Context) OpenPilotRepositoryStatus
}

type OpenPilotRepositoryStatus struct {
	ActivationMode string                              `json:"activation_mode"`
	Configured     bool                                `json:"configured"`
	Healthy        bool                                `json:"healthy"`
	Repositories   []OpenPilotRepositoryExecutorStatus `json:"repositories"`
	CheckedAt      time.Time                           `json:"checked_at"`
	Error          string                              `json:"error,omitempty"`
}

type OpenPilotRepositoryExecutorStatus struct {
	RepositoryID              string                 `json:"repository_id"`
	RepositorySlug            string                 `json:"repository_slug"`
	ExecutorGeneration        string                 `json:"executor_generation"`
	Healthy                   bool                   `json:"healthy"`
	OpenCode                  OpenCodeExecutorStatus `json:"opencode"`
	VerifierHealthy           bool                   `json:"verifier_healthy"`
	VerifierStatus            string                 `json:"verifier_status"`
	MaxConcurrentVerification int                    `json:"max_concurrent_verification"`
}

type envOpenPilotRepositoryStatusProvider struct {
	gate    *OpenPilotAdmissionGate
	bundles map[string]openPilotExecutionBundle
	err     error
	client  *http.Client
}

func NewEnvOpenPilotRepositoryStatusProvider() OpenPilotRepositoryStatusProvider {
	provider := &envOpenPilotRepositoryStatusProvider{client: &http.Client{Timeout: 2 * time.Second}}
	provider.gate, provider.err = NewEnvOpenPilotAdmissionGate()
	if provider.err == nil {
		provider.bundles, provider.err = newOpenPilotExecutionBundles(provider.gate)
	}
	return provider
}

func (p *envOpenPilotRepositoryStatusProvider) Status(ctx context.Context) OpenPilotRepositoryStatus {
	status := OpenPilotRepositoryStatus{Repositories: []OpenPilotRepositoryExecutorStatus{}, CheckedAt: time.Now().UTC()}
	if p == nil || p.err != nil || p.gate == nil {
		status.Error = "Open Pilot repository status is unavailable"
		return status
	}
	status.ActivationMode = p.gate.mode
	status.Configured = len(p.bundles) > 0
	if !status.Configured {
		status.Healthy = true
		return status
	}
	keys := make([]string, 0, len(p.bundles))
	for key := range p.bundles {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	repositories := make([]OpenPilotRepositoryExecutorStatus, len(keys))
	valid := make([]bool, len(keys))
	var probes sync.WaitGroup
	for index, key := range keys {
		bundle := p.bundles[key]
		executor, ok := bundle.openCodeExecutor.(*EnvOpenCodeExecutor)
		if !ok {
			continue
		}
		valid[index] = true
		probes.Add(1)
		go func() {
			defer probes.Done()
			var openCode OpenCodeExecutorStatus
			var verifierHealthy bool
			var verifierStatus string
			var services sync.WaitGroup
			services.Add(2)
			go func() {
				defer services.Done()
				openCode = (&EnvOpenCodeExecutorStatusProvider{
					openCodeURL: executor.baseURL, username: executor.username, password: executor.password,
					backend: "native", client: p.client,
				}).Status(ctx)
			}()
			go func() {
				defer services.Done()
				verifierHealthy, verifierStatus = p.verifierStatus(ctx, bundle.repository.VerifierEndpoint)
			}()
			services.Wait()
			repositories[index] = OpenPilotRepositoryExecutorStatus{
				RepositoryID: bundle.repository.ID, RepositorySlug: bundle.repository.Slug,
				ExecutorGeneration: bundle.repository.ExecutorGeneration,
				Healthy:            openCode.Healthy && verifierHealthy, OpenCode: openCode,
				VerifierHealthy: verifierHealthy, VerifierStatus: verifierStatus,
				MaxConcurrentVerification: 1,
			}
		}()
	}
	probes.Wait()
	status.Healthy = true
	for index, repository := range repositories {
		if !valid[index] {
			status.Healthy = false
			continue
		}
		status.Healthy = status.Healthy && repository.Healthy
		status.Repositories = append(status.Repositories, repository)
	}
	return status
}

func (p *envOpenPilotRepositoryStatusProvider) verifierStatus(ctx context.Context, baseURL string) (bool, string) {
	endpoint, err := url.JoinPath(baseURL, "/healthz")
	if err != nil {
		return false, "invalid endpoint"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, "request failed"
	}
	response, err := p.client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return false, "timeout"
		}
		return false, "unreachable"
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return false, response.Status
	}
	return true, "healthy"
}
