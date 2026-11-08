// Package metrics exposes collector snapshots as Prometheus metrics.
package metrics

import (
	"maps"
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/mcsrobert/openbao-rotation-exporter/internal/collector"
)

var (
	keyDueDesc = prometheus.NewDesc(
		"openbao_rotation_key_due_timestamp_seconds",
		"When the key is due for rotation: the current version's creation time plus the configured interval. Keeps its last known value while reads fail.",
		[]string{"key"}, nil,
	)
	keyUpDesc = prometheus.NewDesc(
		"openbao_rotation_key_up",
		"Whether the most recent read of the key succeeded.",
		[]string{"key"}, nil,
	)
	keyFailuresDesc = prometheus.NewDesc(
		"openbao_rotation_key_failures_total",
		"Failed reads of the key by reason.",
		[]string{"key", "reason"}, nil,
	)
	collectionSuccessDesc = prometheus.NewDesc(
		"openbao_rotation_collection_success",
		"Whether the latest collection authenticated and read at least one key.",
		nil, nil,
	)
	lastSuccessDesc = prometheus.NewDesc(
		"openbao_rotation_last_success_timestamp_seconds",
		"When a collection last succeeded, or 0 if none has since the process started.",
		nil, nil,
	)
	collectionDurationDesc = prometheus.NewDesc(
		"openbao_rotation_collection_duration_seconds",
		"How long the latest collection took.",
		nil, nil,
	)
	collectionFailuresDesc = prometheus.NewDesc(
		"openbao_rotation_collection_failures_total",
		"Failed collections by reason.",
		[]string{"reason"}, nil,
	)
	buildInfoDesc = prometheus.NewDesc(
		"openbao_rotation_build_info",
		"Build information about the exporter.",
		[]string{"version"}, nil,
	)
)

type exporter struct {
	snapshot func() *collector.Snapshot
	version  string
}

// NewRegistry returns a registry serving snapshots from the given function,
// along with the standard Go and process metrics.
func NewRegistry(snapshot func() *collector.Snapshot, version string) *prometheus.Registry {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		&exporter{snapshot: snapshot, version: version},
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return registry
}

func (e *exporter) Describe(descs chan<- *prometheus.Desc) {
	descs <- keyDueDesc
	descs <- keyUpDesc
	descs <- keyFailuresDesc
	descs <- collectionSuccessDesc
	descs <- lastSuccessDesc
	descs <- collectionDurationDesc
	descs <- collectionFailuresDesc
	descs <- buildInfoDesc
}

func (e *exporter) Collect(out chan<- prometheus.Metric) {
	snapshot := e.snapshot()

	gauge := func(desc *prometheus.Desc, value float64, labels ...string) {
		out <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value, labels...)
	}
	counters := func(desc *prometheus.Desc, failures map[string]uint64, labels ...string) {
		for _, reason := range slices.Sorted(maps.Keys(failures)) {
			out <- prometheus.MustNewConstMetric(desc, prometheus.CounterValue, float64(failures[reason]), append(slices.Clone(labels), reason)...)
		}
	}

	for _, status := range snapshot.Keys {
		gauge(keyUpDesc, boolValue(status.Up), status.Key)
		if !status.Due.IsZero() {
			gauge(keyDueDesc, timestamp(status.Due), status.Key)
		}
		counters(keyFailuresDesc, status.Failures, status.Key)
	}
	gauge(collectionSuccessDesc, boolValue(snapshot.Success))
	lastSuccess := 0.0
	if !snapshot.LastSuccess.IsZero() {
		lastSuccess = timestamp(snapshot.LastSuccess)
	}
	gauge(lastSuccessDesc, lastSuccess)
	gauge(collectionDurationDesc, snapshot.Duration.Seconds())
	counters(collectionFailuresDesc, snapshot.Failures)
	gauge(buildInfoDesc, 1, e.version)
}

func boolValue(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func timestamp(t time.Time) float64 {
	return float64(t.UnixMilli()) / 1000
}
