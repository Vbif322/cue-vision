package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	for _, key := range []string{"HTTP_ADDR", "DATABASE_URL", "API_TOKEN", "DETECTORS", "AUTO_TRAINING",
		"SKIP_EVENT_TYPES", "MAX_BODY_BYTES", "DETECTOR_IDLE_TIMEOUT", "LOG_LEVEL",
		"ADMIN_LOGIN", "ADMIN_PASSWORD", "SESSION_TTL", "COOKIE_SECURE"} {
		t.Setenv(key, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8080" || !cfg.AutoTraining || !cfg.SkipTypes["positions"] || len(cfg.Detectors) != 0 ||
		cfg.SessionTTL != 30*24*time.Hour || cfg.CookieSecure {
		t.Fatalf("значения по умолчанию: %+v", cfg)
	}
}

func TestLoadDetectors(t *testing.T) {
	t.Setenv("DETECTORS", " club1-t1=ws://10.0.0.5:8765 , club1-t2=wss://cam.example/ws ")
	t.Setenv("SKIP_EVENT_TYPES", "positions, state")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Detectors) != 2 || cfg.Detectors[0].Stream != "club1-t1" || cfg.Detectors[1].URL != "wss://cam.example/ws" {
		t.Fatalf("детекторы: %+v", cfg.Detectors)
	}
	if !cfg.SkipTypes["state"] {
		t.Fatalf("пропускаемые типы: %+v", cfg.SkipTypes)
	}
}

func TestLoadRejects(t *testing.T) {
	for name, env := range map[string][2]string{
		"адрес без схемы ws":   {"DETECTORS", "t1=http://host:1"},
		"нет имени потока":     {"DETECTORS", "ws://host:1"},
		"плохое имя потока":    {"DETECTORS", "a/b=ws://host:1"},
		"поток указан дважды":  {"DETECTORS", "t1=ws://a:1,t1=ws://b:1"},
		"не булево значение":   {"AUTO_TRAINING", "maybe"},
		"неизвестный уровень":  {"LOG_LEVEL", "loud"},
		"отрицательный предел": {"MAX_BODY_BYTES", "-1"},
		"логин без пароля":     {"ADMIN_LOGIN", "admin"},
		"пароль без логина":    {"ADMIN_PASSWORD", "secret-password"},
		"короткая сессия":      {"SESSION_TTL", "10s"},
		"cookie не булево":     {"COOKIE_SECURE", "sometimes"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(env[0], env[1])
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%q: ожидалась ошибка", env[0], env[1])
			}
		})
	}
}
