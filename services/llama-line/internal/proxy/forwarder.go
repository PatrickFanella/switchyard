package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
)

// OllamaUnavailableRecorder is an optional hook called on each ollama retry.
type OllamaUnavailableRecorder interface {
	RecordOllamaUnavailable()
}

// OllamaUnavailableAlerter is an optional hook for sending alerts on ollama unavailability.
type OllamaUnavailableAlerter interface {
	AlertOllamaUnavailable()
}

// Forwarder proxies requests to the upstream ollama instance.
type Forwarder struct {
	OllamaURL    string
	maxRetrySecs int
	client       *http.Client
	log          *slog.Logger
	met          OllamaUnavailableRecorder // nil if metrics disabled
	alerter      OllamaUnavailableAlerter  // nil if alerting disabled
	outboundAuth config.UpstreamAuthConfig
}

// HealthCheckResult is the cached-safe result of probing an upstream.
type HealthCheckResult struct {
	Healthy   bool
	CheckedAt time.Time
	Duration  time.Duration
	Error     string
}

// New creates a Forwarder targeting ollamaURL.
func New(ollamaURL string, maxRetrySecs int, log *slog.Logger) *Forwarder {
	return &Forwarder{
		OllamaURL:    ollamaURL,
		maxRetrySecs: maxRetrySecs,
		client: &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				DisableCompression: true,
			},
		},
		log: log,
	}
}

// SetMetrics attaches an OllamaUnavailableRecorder to the forwarder.
func (f *Forwarder) SetMetrics(m OllamaUnavailableRecorder) {
	f.met = m
}

// SetAlerter attaches an OllamaUnavailableAlerter to the forwarder.
func (f *Forwarder) SetAlerter(a OllamaUnavailableAlerter) {
	f.alerter = a
}

// SetOutboundAuth configures credentials added to requests sent to this upstream.
func (f *Forwarder) SetOutboundAuth(auth config.UpstreamAuthConfig) {
	f.outboundAuth = auth
}

// Forward implements queue.ForwardFn.
func (f *Forwarder) Forward(ctx context.Context, r *queue.Request) queue.Result {
	target, err := buildTargetURL(f.OllamaURL, r.HTTPReq.URL)
	if err != nil {
		return queue.Result{Err: fmt.Errorf("proxy: build URL: %w", err)}
	}

	// Buffer the body so it can be replayed on retry.
	var bodyBytes []byte
	if r.HTTPReq.Body != nil {
		bodyBytes, err = io.ReadAll(r.HTTPReq.Body)
		if err != nil {
			return queue.Result{Err: fmt.Errorf("proxy: read body: %w", err)}
		}
	}

	backoff := time.Second
	maxBackoff := time.Duration(f.maxRetrySecs) * time.Second

	for {
		var bodyReader io.Reader
		if bodyBytes != nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, r.HTTPReq.Method, target, bodyReader)
		if err != nil {
			return queue.Result{Err: fmt.Errorf("proxy: clone request: %w", err)}
		}
		for k, vv := range r.HTTPReq.Header {
			if strings.EqualFold(k, "Authorization") {
				continue
			}
			for _, v := range vv {
				req.Header.Add(k, v)
			}
		}
		if r.ID != "" {
			req.Header.Set("X-Llama-Line-Request-ID", r.ID)
		}
		f.applyOutboundAuth(req)

		resp, err := f.client.Do(req)
		if err == nil {
			return queue.Result{Resp: resp}
		}

		if isContextError(err) || ctx.Err() != nil {
			return queue.Result{Err: err}
		}

		// Only retry on connection-level errors.
		if !isConnError(err) {
			return queue.Result{Err: err}
		}

		f.log.Warn("ollama unavailable, retrying", "backoff", backoff)
		select {
		case r.StatusCh <- queue.StatusUpdate{Status: "ollama_unavailable", Position: 1}:
		default:
		}
		if f.met != nil {
			f.met.RecordOllamaUnavailable()
		}
		if f.alerter != nil {
			f.alerter.AlertOllamaUnavailable()
		}

		select {
		case <-ctx.Done():
			return queue.Result{Err: ctx.Err()}
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// HealthCheck probes the upstream with a lightweight GET request.
func (f *Forwarder) HealthCheck(ctx context.Context, healthPath string) HealthCheckResult {
	checkedAt := time.Now()
	healthPath = strings.TrimSpace(healthPath)
	if healthPath == "" {
		healthPath = "/api/version"
	}
	if !strings.HasPrefix(healthPath, "/") {
		healthPath = "/" + healthPath
	}

	parsedPath, err := url.ParseRequestURI(healthPath)
	if err != nil {
		return HealthCheckResult{CheckedAt: checkedAt, Error: fmt.Sprintf("parse health path: %v", err)}
	}

	target, err := buildTargetURL(f.OllamaURL, parsedPath)
	if err != nil {
		return HealthCheckResult{CheckedAt: checkedAt, Error: fmt.Sprintf("build URL: %v", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return HealthCheckResult{CheckedAt: checkedAt, Error: fmt.Sprintf("build request: %v", err)}
	}
	f.applyOutboundAuth(req)

	start := time.Now()
	resp, err := f.client.Do(req)
	duration := time.Since(start)
	if err != nil {
		return HealthCheckResult{CheckedAt: checkedAt, Duration: duration, Error: err.Error()}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return HealthCheckResult{CheckedAt: checkedAt, Duration: duration, Error: fmt.Sprintf("status %d", resp.StatusCode)}
	}
	return HealthCheckResult{Healthy: true, CheckedAt: checkedAt, Duration: duration}
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func buildTargetURL(base string, orig *url.URL) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	b.Path = orig.Path
	b.RawQuery = orig.RawQuery
	return b.String(), nil
}

func isConnError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// connection refused, no such host, etc.
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

func (f *Forwarder) applyOutboundAuth(req *http.Request) {
	switch f.outboundAuth.Type {
	case "bearer":
		if f.outboundAuth.Token != "" {
			req.Header.Set("Authorization", "Bearer "+f.outboundAuth.Token)
		}
	}
}
