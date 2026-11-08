// Package server provides the exporter's HTTP endpoints.
package server

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// New returns an HTTP server for /metrics, /healthz and /readyz. ready reports
// whether /metrics has data worth scraping. No endpoint triggers a collection
// or contacts OpenBao.
func New(address string, gatherer prometheus.Gatherer, ready func() bool) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           Handler(gatherer, ready),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}

// Handler returns the exporter's routes.
func Handler(gatherer prometheus.Gatherer, ready func() bool) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}
