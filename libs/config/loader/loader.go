// Package loader provides a Viper-based configuration loader used by every
// service in the registry platform. It defines shared config structs and a
// Load helper so each service's internal/config package doesn't repeat boilerplate.
package loader

import (
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/viper"
)

// BaseConfig contains fields that every service needs.
// Each service embeds this struct in its own Config type.
type BaseConfig struct {
	LogLevel  string `mapstructure:"LOG_LEVEL"`
	LogFormat string `mapstructure:"LOG_FORMAT"`

	// gRPC and HTTP listener addresses
	GRPCAddr string `mapstructure:"GRPC_ADDR"`
	HTTPAddr string `mapstructure:"HTTP_ADDR"`

	// mTLS certificate paths — required by all gRPC servers and clients
	MTLSCACertPath string `mapstructure:"MTLS_CA_CERT_PATH"`
	MTLSCertPath   string `mapstructure:"MTLS_CERT_PATH"`
	MTLSKeyPath    string `mapstructure:"MTLS_KEY_PATH"`

	// OpenTelemetry settings
	OTELExporter     string  `mapstructure:"OTEL_EXPORTER"`
	OTELEndpoint     string  `mapstructure:"OTEL_ENDPOINT"`
	OTELServiceName  string  `mapstructure:"OTEL_SERVICE_NAME"`
	OTELEnvironment  string  `mapstructure:"OTEL_ENVIRONMENT"`
	OTELSamplingRate float64 `mapstructure:"OTEL_SAMPLING_RATE"`
}

// DBConfig contains PostgreSQL connection and pool settings.
// Only services that own a database (auth, metadata, tenant, audit) embed this.
// Pool defaults implement REM-006: prevent exhaustion and map acquire timeouts
// to codes.ResourceExhausted rather than hanging forever.
type DBConfig struct {
	DBDSN        string `mapstructure:"DB_DSN"`
	DBDSNReplica string `mapstructure:"DB_DSN_REPLICA"` // optional read replica

	// Pool sizing — tune via env; defaults are safe for most workloads
	DBMaxConns int32 `mapstructure:"DB_MAX_CONNS"`
	DBMinConns int32 `mapstructure:"DB_MIN_CONNS"`

	// Timeouts — kept short so pool exhaustion surfaces as ResourceExhausted quickly
	DBConnectTimeout  time.Duration `mapstructure:"DB_CONNECT_TIMEOUT"`
	DBMaxConnLifetime time.Duration `mapstructure:"DB_MAX_CONN_LIFETIME"`
	DBMaxConnIdleTime time.Duration `mapstructure:"DB_MAX_CONN_IDLE_TIME"`
}

// PoolConfig constructs a pgxpool.Config from DBConfig ready for pgxpool.NewWithConfig.
// Enforces that DB_DSN includes sslmode=require — sslmode=disable is rejected at startup.
func (c *DBConfig) PoolConfig() (*pgxpool.Config, error) {
	if c.DBDSN == "" {
		return nil, fmt.Errorf("DB_DSN is required")
	}
	// sslmode=disable would silently transmit passwords in cleartext
	dsn := strings.ToLower(c.DBDSN)
	if strings.Contains(dsn, "sslmode=disable") || !strings.Contains(dsn, "sslmode=") {
		return nil, fmt.Errorf("DB_DSN must include sslmode=require; sslmode=disable is not permitted")
	}
	cfg, err := pgxpool.ParseConfig(c.DBDSN)
	if err != nil {
		return nil, fmt.Errorf("parse DB_DSN: %w", err)
	}
	if c.DBMaxConns > 0 {
		cfg.MaxConns = c.DBMaxConns
	}
	if c.DBMinConns > 0 {
		cfg.MinConns = c.DBMinConns
	}
	if c.DBConnectTimeout > 0 {
		cfg.ConnConfig.ConnectTimeout = c.DBConnectTimeout
	}
	if c.DBMaxConnLifetime > 0 {
		cfg.MaxConnLifetime = c.DBMaxConnLifetime
	}
	if c.DBMaxConnIdleTime > 0 {
		cfg.MaxConnIdleTime = c.DBMaxConnIdleTime
	}
	return cfg, nil
}

// Load binds environment variables into cfg using Viper and applies
// service-agnostic defaults. cfg must be a pointer to a mapstructure-tagged struct.
// serviceName sets the default for OTEL_SERVICE_NAME when the env var is absent.
func Load(serviceName string, cfg any) error {
	v := viper.New()
	v.AutomaticEnv()

	v.SetDefault("LOG_LEVEL", "info")
	v.SetDefault("LOG_FORMAT", "json")
	v.SetDefault("GRPC_ADDR", ":50051")
	v.SetDefault("HTTP_ADDR", ":8080")
	v.SetDefault("OTEL_SERVICE_NAME", serviceName)
	v.SetDefault("OTEL_SAMPLING_RATE", 1.0)

	// DB pool defaults — these match the REM-006 recommended values
	v.SetDefault("DB_MAX_CONNS", 20)
	v.SetDefault("DB_MIN_CONNS", 2)
	v.SetDefault("DB_CONNECT_TIMEOUT", "5s")
	v.SetDefault("DB_MAX_CONN_LIFETIME", "30m")
	v.SetDefault("DB_MAX_CONN_IDLE_TIME", "5m")

	if err := v.Unmarshal(cfg); err != nil {
		return fmt.Errorf("unmarshal config: %w", err)
	}
	return nil
}

// RequireFields returns an error listing the names of any required config fields
// whose values are empty. Pass a map of env-var-name → current-value pairs.
//
//	loader.RequireFields(map[string]string{
//	    "MTLS_CA_CERT_PATH": cfg.MTLSCACertPath,
//	    "JWT_PRIVATE_KEY":   cfg.JWTPrivateKey,
//	})
func RequireFields(fields map[string]string) error {
	var missing []string
	for name, value := range fields {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("required env vars not set: %v", missing)
}
