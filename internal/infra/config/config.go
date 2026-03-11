package config

import (
	"os"
	"strconv"
)

type Config struct {
	Port                 string
	Env                  string
	PublicBaseURL        string
	DatabaseURL          string
	MigrationsDir        string
	MigrationsAutoApply  bool
	MaxPayloadBytes      int64
	ReadTimeoutSeconds   int
	WriteTimeoutSeconds  int
	CORSAllowedOrigins   string
	CORSAllowedMethods   string
	CORSAllowedHeaders   string
	CORSMaxAgeSeconds    int
	DefaultRetentionDays int
	AdminRateLimitRPM    int
	RequireAccess        bool
	AccessAudience       string
	CronSecret           string
}

func Load() Config {
	return Config{
		Port:                 envOrDefault("PORT", "8080"),
		Env:                  envOrDefault("ENV", "development"),
		PublicBaseURL:        os.Getenv("PUBLIC_BASE_URL"),
		DatabaseURL:          os.Getenv("DATABASE_URL"),
		MigrationsDir:        envOrDefault("MIGRATIONS_DIR", "migrations"),
		MigrationsAutoApply:  envBool("MIGRATIONS_AUTO_APPLY", true),
		MaxPayloadBytes:      int64(envInt("MAX_PAYLOAD_BYTES", 1024*1024)),
		ReadTimeoutSeconds:   envInt("READ_TIMEOUT_SECONDS", 10),
		WriteTimeoutSeconds:  envInt("WRITE_TIMEOUT_SECONDS", 10),
		CORSAllowedOrigins:   envOrDefault("CORS_ALLOWED_ORIGINS", "http://localhost:3000,http://localhost:5173,http://localhost:4173"),
		CORSAllowedMethods:   envOrDefault("CORS_ALLOWED_METHODS", "GET,POST,PATCH,DELETE,OPTIONS"),
		CORSAllowedHeaders:   envOrDefault("CORS_ALLOWED_HEADERS", "Content-Type,Authorization,CF-Access-Authenticated-User-Email,CF-Access-Jwt-Assertion,CF-Access-Client-Id,CF-Access-Client-Secret,X-Request-ID"),
		CORSMaxAgeSeconds:    envInt("CORS_MAX_AGE_SECONDS", 600),
		DefaultRetentionDays: envInt("DEFAULT_RETENTION_DAYS", 30),
		AdminRateLimitRPM:    envInt("ADMIN_RATE_LIMIT_RPM", 120),
		RequireAccess:        envBool("REQUIRE_ACCESS", true),
		AccessAudience:       os.Getenv("ACCESS_AUDIENCE"),
		CronSecret:           os.Getenv("CRON_SECRET"),
	}
}

func envOrDefault(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func envInt(k string, fallback int) int {
	v := os.Getenv(k)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envBool(k string, fallback bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}
