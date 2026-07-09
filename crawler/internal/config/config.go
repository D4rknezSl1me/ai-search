// Package config loads crawler configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	// Postgres
	PGHost, PGPort, PGUser, PGPassword, PGDatabase string
	// MinIO (blob store for raw content)
	MinIOHost, MinIOPort, MinIOUser, MinIOPassword string
	MinIOBucket                                    string
	// Redis / NATS (reachability only in Phase 1)
	RedisHost, RedisPort string
	NATSHost, NATSPort   string
	// HTTP control/health API
	HealthPort string
	// Crawl tuning
	Workers      int
	MinDelayMs   int
	FetchTimeout int // seconds
	MaxBodyBytes int64
	UserAgent    string
	ReapAfterS   int // requeue URLs stuck in FETCHING past this many seconds (0 = off)
	// Freshness scheduler (Phase 3): re-ingest tracked social entities on cadence.
	FreshnessTickS int // how often to look for due tracked entities (0 = scheduler off)
	FreshnessBatch int // max entities claimed per tick
	SocialPaceMs   int // delay between successive social fetches within a run (politeness)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func Load() *Config {
	return &Config{
		PGHost:     env("POSTGRES_HOST", "postgres"),
		PGPort:     env("POSTGRES_PORT", "5432"),
		PGUser:     env("POSTGRES_USER", "aisearch"),
		PGPassword: env("POSTGRES_PASSWORD", "aisearch"),
		PGDatabase: env("POSTGRES_DB", "aisearch"),

		MinIOHost:     env("MINIO_HOST", "minio"),
		MinIOPort:     env("MINIO_PORT", "9000"),
		MinIOUser:     env("MINIO_ROOT_USER", "aisearch"),
		MinIOPassword: env("MINIO_ROOT_PASSWORD", "aisearch"),
		MinIOBucket:   env("MINIO_BUCKET", "raw"),

		RedisHost: env("REDIS_HOST", "redis"),
		RedisPort: env("REDIS_PORT", "6379"),
		NATSHost:  env("NATS_HOST", "nats"),
		NATSPort:  env("NATS_PORT", "4222"),

		HealthPort: env("CRAWLER_HEALTH_PORT", "8090"),

		Workers:      envInt("CRAWLER_WORKERS", 8),
		MinDelayMs:   envInt("CRAWLER_MIN_DELAY_MS", 800),
		FetchTimeout: envInt("CRAWLER_FETCH_TIMEOUT_S", 15),
		MaxBodyBytes: int64(envInt("CRAWLER_MAX_BODY_BYTES", 5_000_000)),
		UserAgent:    env("CRAWLER_USER_AGENT", "Mozilla/5.0 (compatible; ai-search/0.1)"),
		ReapAfterS:   envInt("CRAWLER_REAP_AFTER_S", 300),

		FreshnessTickS: envInt("CRAWLER_FRESHNESS_TICK_S", 30),
		FreshnessBatch: envInt("CRAWLER_FRESHNESS_BATCH", 16),
		SocialPaceMs:   envInt("CRAWLER_SOCIAL_PACE_MS", 500),
	}
}

// PGConnString returns a libpq-style DSN.
func (c *Config) PGConnString() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		c.PGUser, c.PGPassword, c.PGHost, c.PGPort, c.PGDatabase)
}

func (c *Config) MinIOEndpoint() string {
	return fmt.Sprintf("%s:%s", c.MinIOHost, c.MinIOPort)
}
