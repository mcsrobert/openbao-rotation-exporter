package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/mcsrobert/openbao-rotation-exporter/internal/collector"
)

const exporterMetrics = `
# HELP openbao_rotation_build_info Build information about the exporter.
# TYPE openbao_rotation_build_info gauge
openbao_rotation_build_info{version="1.2.3"} 1
# HELP openbao_rotation_collection_duration_seconds How long the latest collection took.
# TYPE openbao_rotation_collection_duration_seconds gauge
openbao_rotation_collection_duration_seconds 1.5
# HELP openbao_rotation_collection_failures_total Failed collections by reason.
# TYPE openbao_rotation_collection_failures_total counter
openbao_rotation_collection_failures_total{reason="unavailable"} 2
# HELP openbao_rotation_collection_interval_seconds The configured time between collections.
# TYPE openbao_rotation_collection_interval_seconds gauge
openbao_rotation_collection_interval_seconds 3600
# HELP openbao_rotation_collection_success Whether the latest collection authenticated and read at least one key.
# TYPE openbao_rotation_collection_success gauge
openbao_rotation_collection_success 1
# HELP openbao_rotation_key_due_timestamp_seconds When the key is due for rotation: the current version's creation time plus the configured interval. Keeps its last known value while reads fail.
# TYPE openbao_rotation_key_due_timestamp_seconds gauge
openbao_rotation_key_due_timestamp_seconds{key="app/\"db\""} 1.7672256005e+09
# HELP openbao_rotation_key_failures_total Failed reads of the key by reason.
# TYPE openbao_rotation_key_failures_total counter
openbao_rotation_key_failures_total{key="app/\"db\"",reason="forbidden"} 3
openbao_rotation_key_failures_total{key="app/\"db\"",reason="timeout"} 1
# HELP openbao_rotation_key_up Whether the most recent read of the key succeeded.
# TYPE openbao_rotation_key_up gauge
openbao_rotation_key_up{key="app/\"db\""} 0
# HELP openbao_rotation_last_success_timestamp_seconds When a collection last succeeded, or 0 if none has since the process started.
# TYPE openbao_rotation_last_success_timestamp_seconds gauge
openbao_rotation_last_success_timestamp_seconds 1.767225601e+09
`

var exporterMetricNames = []string{
	"openbao_rotation_build_info",
	"openbao_rotation_collection_duration_seconds",
	"openbao_rotation_collection_failures_total",
	"openbao_rotation_collection_interval_seconds",
	"openbao_rotation_collection_success",
	"openbao_rotation_key_due_timestamp_seconds",
	"openbao_rotation_key_failures_total",
	"openbao_rotation_key_up",
	"openbao_rotation_last_success_timestamp_seconds",
}

func TestExposition(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snapshot := &collector.Snapshot{
		Attempted:   true,
		Success:     true,
		LastSuccess: base.Add(time.Second),
		Duration:    1500 * time.Millisecond,
		Interval:    time.Hour,
		Failures:    map[string]uint64{"unavailable": 2},
		Keys: []collector.KeyStatus{
			{Key: `app/"db"`, ReadAttempted: true, Due: base.Add(500 * time.Millisecond), Failures: map[string]uint64{"forbidden": 3, "timeout": 1}},
			{Key: "never-read", Failures: map[string]uint64{}},
		},
	}
	registry := NewRegistry(func() *collector.Snapshot { return snapshot }, "1.2.3")

	err := testutil.GatherAndCompare(registry, strings.NewReader(exporterMetrics), exporterMetricNames...)
	if err != nil {
		t.Fatal(err)
	}
}

func TestInitialSnapshotHasZeroLastSuccess(t *testing.T) {
	snapshot := &collector.Snapshot{Keys: []collector.KeyStatus{{Key: "a"}}}
	registry := NewRegistry(func() *collector.Snapshot { return snapshot }, "dev")

	const want = `
# HELP openbao_rotation_last_success_timestamp_seconds When a collection last succeeded, or 0 if none has since the process started.
# TYPE openbao_rotation_last_success_timestamp_seconds gauge
openbao_rotation_last_success_timestamp_seconds 0
`
	err := testutil.GatherAndCompare(registry, strings.NewReader(want), "openbao_rotation_last_success_timestamp_seconds")
	if err != nil {
		t.Fatal(err)
	}
}

func TestMetricsPassLint(t *testing.T) {
	snapshot := &collector.Snapshot{
		Failures: map[string]uint64{"unavailable": 1},
		Keys:     []collector.KeyStatus{{Key: "a", Due: time.Now(), Failures: map[string]uint64{"forbidden": 1}}},
	}
	exporter := &exporter{snapshot: func() *collector.Snapshot { return snapshot }, version: "dev"}
	problems, err := testutil.CollectAndLint(exporter)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("lint problems: %v", problems)
	}
}
