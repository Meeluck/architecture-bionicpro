package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type config struct {
	Port               string
	KeycloakIssuer     string
	KeycloakJWKSURL    string
	JWTAudience        string
	JWTRequiredRole    string
	ClickHouseURL      string
	ClickHouseDB       string
	ClickHouseUser     string
	ClickHousePassword string
	CORSAllowedOrigin  string
	DefaultReportRange time.Duration
	MaxReportRange     time.Duration
}

func loadConfig() (config, error) {
	defaultRange, err := durationFromHours("DEFAULT_REPORT_RANGE_HOURS", 24*30)
	if err != nil {
		return config{}, err
	}
	maxRange, err := durationFromHours("MAX_REPORT_RANGE_HOURS", 24*31)
	if err != nil {
		return config{}, err
	}
	if defaultRange > maxRange {
		return config{}, fmt.Errorf("DEFAULT_REPORT_RANGE_HOURS must not exceed MAX_REPORT_RANGE_HOURS")
	}

	cfg := config{
		Port:               envOrDefault("PORT", "8000"),
		KeycloakIssuer:     envOrDefault("KEYCLOAK_ISSUER", "http://localhost:8080/realms/reports-realm"),
		KeycloakJWKSURL:    envOrDefault("KEYCLOAK_JWKS_URL", "http://keycloak:8080/realms/reports-realm/protocol/openid-connect/certs"),
		JWTAudience:        envOrDefault("JWT_AUDIENCE", "reports-api"),
		JWTRequiredRole:    envOrDefault("JWT_REQUIRED_ROLE", "prothetic_user"),
		ClickHouseURL:      envOrDefault("CLICKHOUSE_URL", "http://clickhouse:8123"),
		ClickHouseDB:       envOrDefault("CLICKHOUSE_DB", "bionicpro"),
		ClickHouseUser:     envOrDefault("CLICKHOUSE_USER", "reports_api"),
		ClickHousePassword: envOrDefault("CLICKHOUSE_PASSWORD", "reports_api_password"),
		CORSAllowedOrigin:  envOrDefault("CORS_ALLOWED_ORIGIN", "http://localhost:3000"),
		DefaultReportRange: defaultRange,
		MaxReportRange:     maxRange,
	}

	for name, value := range map[string]string{
		"KEYCLOAK_ISSUER":   cfg.KeycloakIssuer,
		"KEYCLOAK_JWKS_URL": cfg.KeycloakJWKSURL,
		"JWT_AUDIENCE":      cfg.JWTAudience,
		"CLICKHOUSE_URL":    cfg.ClickHouseURL,
	} {
		if value == "" {
			return config{}, fmt.Errorf("%s must not be empty", name)
		}
	}

	return cfg, nil
}

func envOrDefault(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	return fallback
}

func durationFromHours(name string, fallback int) (time.Duration, error) {
	raw := envOrDefault(name, strconv.Itoa(fallback))
	hours, err := strconv.Atoi(raw)
	if err != nil || hours <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return time.Duration(hours) * time.Hour, nil
}
