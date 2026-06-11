// Package metrics defines the standard Prometheus instruments shared across all registry services.
// Each service exposes these at GET /metrics (internal port only — not reachable via the gateway).
// Instruments are registered against prometheus.DefaultRegisterer via promauto so they are
// automatically available through the Handler() scrape endpoint without any extra wiring.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// HTTPRequestDuration tracks HTTP handler latency per service, method, path, and status code.
	// Use this to observe p50/p95/p99 latencies across all HTTP-serving services.
	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "registry_http_request_duration_seconds",
		Help:    "HTTP request duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"service", "method", "path", "status"})

	// GRPCRequestDuration tracks gRPC handler latency per service, method, and gRPC status code.
	GRPCRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "registry_grpc_request_duration_seconds",
		Help:    "gRPC request duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"service", "method", "status"})

	// RabbitMQMessagesConsumed counts RabbitMQ messages processed, labelled by queue and outcome.
	// "status" is either "success" or "error".
	RabbitMQMessagesConsumed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "registry_rabbitmq_messages_consumed_total",
		Help: "Total RabbitMQ messages consumed.",
	}, []string{"service", "queue", "status"})

	// StorageOperationDuration tracks storage driver operation latency per driver, operation, and outcome.
	// Use to identify slow blob I/O paths across MinIO/S3/GCS/Azure backends.
	StorageOperationDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "registry_storage_operation_duration_seconds",
		Help:    "Storage driver operation duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"driver", "operation", "status"})

	// ActiveUploads is a gauge that tracks the number of in-progress chunked blob uploads.
	// A sustained high value may indicate stalled uploads consuming Redis state.
	ActiveUploads = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "registry_active_uploads_total",
		Help: "Number of blob uploads currently in progress.",
	})
)

// Handler returns an http.Handler that serves the default Prometheus registry.
// Mount this at /metrics on each service's internal HTTP port.
func Handler() http.Handler {
	return promhttp.Handler()
}
