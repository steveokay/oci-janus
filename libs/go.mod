module github.com/steveokay/oci-janus/libs

go 1.23

toolchain go1.23.0

require (
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.6.0
	github.com/redis/go-redis/v9 v9.5.1
	github.com/rabbitmq/amqp091-go v1.10.0
	github.com/spf13/viper v1.19.0
	go.opentelemetry.io/otel v1.28.0
	go.opentelemetry.io/otel/trace v1.28.0
	go.opentelemetry.io/otel/metric v1.28.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.28.0
	go.opentelemetry.io/otel/sdk v1.28.0
	google.golang.org/grpc v1.65.0
	google.golang.org/protobuf v1.34.2
	golang.org/x/crypto v0.25.0
)
