module github.com/steveokay/oci-janus/services/storage

go 1.23

toolchain go1.23.0

require (
	github.com/steveokay/oci-janus/libs v0.0.0
	github.com/google/uuid v1.6.0
	github.com/spf13/viper v1.19.0
	go.opentelemetry.io/otel v1.28.0
	go.opentelemetry.io/otel/trace v1.28.0
	google.golang.org/grpc v1.65.0
	google.golang.org/protobuf v1.34.2
)

replace github.com/steveokay/oci-janus/libs => ../../libs
