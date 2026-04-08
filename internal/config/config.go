package config

import (
	"os"
	"strconv"
)

// Config holds all runtime configuration sourced from environment variables.
type Config struct {
	ListenAddr         string
	AdminAddr          string
	DBPath             string
	DialTimeoutSec     int
	ResponseTimeoutSec int
}

func Load() *Config {
	return &Config{
		ListenAddr:         getEnv("ALB_LISTEN_ADDR", ":8080"),
		AdminAddr:          getEnv("ALB_ADMIN_ADDR", ":9090"),
		DBPath:             getEnv("ALB_DB_PATH", "/data/alb.db"),
		DialTimeoutSec:     getEnvInt("ALB_DIAL_TIMEOUT_SEC", 5),
		ResponseTimeoutSec: getEnvInt("ALB_RESPONSE_TIMEOUT_SEC", 30),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}
