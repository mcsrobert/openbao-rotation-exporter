package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func get(t *testing.T, handler http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), method, path, nil))
	return recorder
}

func TestRoutes(t *testing.T) {
	registry := prometheus.NewRegistry()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "example", Help: "Example."})
	registry.MustRegister(gauge)

	ready := false
	handler := Handler(registry, func() bool { return ready })

	if got := get(t, handler, http.MethodGet, "/healthz").Code; got != http.StatusOK {
		t.Errorf("/healthz = %d", got)
	}
	if got := get(t, handler, http.MethodGet, "/readyz").Code; got != http.StatusServiceUnavailable {
		t.Errorf("/readyz before ready = %d", got)
	}
	ready = true
	if got := get(t, handler, http.MethodGet, "/readyz").Code; got != http.StatusOK {
		t.Errorf("/readyz after ready = %d", got)
	}

	metrics := get(t, handler, http.MethodGet, "/metrics")
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), "example 0") {
		t.Errorf("/metrics = %d %q", metrics.Code, metrics.Body.String())
	}

	if got := get(t, handler, http.MethodGet, "/other").Code; got != http.StatusNotFound {
		t.Errorf("/other = %d", got)
	}
	if got := get(t, handler, http.MethodPost, "/metrics").Code; got != http.StatusMethodNotAllowed {
		t.Errorf("POST /metrics = %d", got)
	}
}

func TestNewSetsTimeouts(t *testing.T) {
	server := New(":8080", prometheus.NewRegistry(), func() bool { return true })
	if server.ReadHeaderTimeout == 0 || server.ReadTimeout == 0 || server.WriteTimeout == 0 {
		t.Fatalf("missing timeouts: %+v", server)
	}
}
