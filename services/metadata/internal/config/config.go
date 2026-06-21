package config

import (
	"fmt"

	"github.com/steveokay/oci-janus/libs/config/loader"
)

// Config is the complete set of environment variables required by registry-metadata.
type Config struct {
	loader.BaseConfig `mapstructure:",squash"`
	loader.DBConfig   `mapstructure:",squash"`

	RedisAddr     string `mapstructure:"REDIS_ADDR"`
	RedisPassword string `mapstructure:"REDIS_PASSWORD"`
	RedisDB       int    `mapstructure:"REDIS_DB"`

	// RabbitMQURL (FE-API-042) — required for the pull.image consumer that
	// drives manifests.last_pulled_at updates. Optional in the sense that an
	// empty string disables the consumer (handy for unit tests + offline
	// dev), but the service logs a WARN on startup when it isn't set.
	RabbitMQURL string `mapstructure:"RABBITMQ_URL"`
}

// Load binds environment variables into Config and validates required fields.
func Load() (*Config, error) {
	cfg := &Config{}
	if err := loader.Load("registry-metadata", cfg); err != nil {
		return nil, err
	}
	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

func validate(cfg *Config) error {
	return loader.RequireFields(map[string]string{
		"DB_DSN":     cfg.DBDSN,
		"REDIS_ADDR": cfg.RedisAddr,
	})
}
