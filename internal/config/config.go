// Package config читает настройки сервиса из переменных окружения.
package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Detector — экземпляр snooker_CV, запущенный с ключом --ws: сервис подключается
// к нему клиентом и читает поток.
type Detector struct {
	Stream string
	URL    string
}

// Config — настройки сервиса.
type Config struct {
	HTTPAddr     string
	DatabaseURL  string
	APIToken     string
	LogLevel     slog.Level
	Detectors    []Detector
	AutoTraining bool
	SkipTypes    map[string]bool
	MaxBodyBytes int64
	IdleTimeout  time.Duration
}

var streamRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidStream проверяет имя потока: оно попадает в путь URL и в ключи блокировок.
func ValidStream(name string) bool { return streamRe.MatchString(name) }

// DefaultStream — поток для событий, присланных без явного имени.
const DefaultStream = "default"

// Load читает настройки и проверяет их.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:    env("HTTP_ADDR", ":8080"),
		DatabaseURL: env("DATABASE_URL", "postgres://cuevision:cuevision@localhost:5432/cuevision?sslmode=disable"),
		APIToken:    os.Getenv("API_TOKEN"),
		SkipTypes:   map[string]bool{},
	}

	if err := cfg.LogLevel.UnmarshalText([]byte(env("LOG_LEVEL", "info"))); err != nil {
		return Config{}, fmt.Errorf("LOG_LEVEL: %w", err)
	}

	var err error
	if cfg.AutoTraining, err = strconv.ParseBool(env("AUTO_TRAINING", "true")); err != nil {
		return Config{}, fmt.Errorf("AUTO_TRAINING: %w", err)
	}
	if cfg.MaxBodyBytes, err = strconv.ParseInt(env("MAX_BODY_BYTES", strconv.Itoa(64<<20)), 10, 64); err != nil || cfg.MaxBodyBytes <= 0 {
		return Config{}, fmt.Errorf("MAX_BODY_BYTES: ожидается положительное число байт")
	}
	if cfg.IdleTimeout, err = time.ParseDuration(env("DETECTOR_IDLE_TIMEOUT", "60s")); err != nil || cfg.IdleTimeout <= 0 {
		return Config{}, fmt.Errorf("DETECTOR_IDLE_TIMEOUT: ожидается длительность вроде 60s")
	}

	for _, t := range splitList(env("SKIP_EVENT_TYPES", "positions")) {
		cfg.SkipTypes[t] = true
	}

	seen := map[string]bool{}
	for _, item := range splitList(os.Getenv("DETECTORS")) {
		stream, raw, ok := strings.Cut(item, "=")
		stream, raw = strings.TrimSpace(stream), strings.TrimSpace(raw)
		if !ok || !ValidStream(stream) {
			return Config{}, fmt.Errorf("DETECTORS: %q — ожидается поток=ws://host:port", item)
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
			return Config{}, fmt.Errorf("DETECTORS: %q — адрес должен начинаться с ws:// или wss://", item)
		}
		if seen[stream] {
			return Config{}, fmt.Errorf("DETECTORS: поток %q указан дважды", stream)
		}
		seen[stream] = true
		cfg.Detectors = append(cfg.Detectors, Detector{Stream: stream, URL: raw})
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
