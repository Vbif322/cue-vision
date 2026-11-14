// Package testdb поднимает для теста чистую схему в PostgreSQL.
//
// Тесты с базой включаются переменной TEST_DATABASE_URL и пропускаются без неё.
// Каждый тест получает собственную схему, поэтому тесты не мешают друг другу и
// не трогают данные в public.
package testdb

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Vbif322/cue-vision/internal/store"
)

// New возвращает хранилище с применёнными миграциями в отдельной схеме.
// Схема удаляется по завершении теста.
func New(t *testing.T) *store.Store {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL не задан — тест с базой пропущен")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	schema := fmt.Sprintf("t_%d_%s", time.Now().UnixNano(), sanitize(t.Name()))
	if len(schema) > 60 {
		schema = schema[:60]
	}
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("подключение к тестовой базе: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("создание схемы: %v", err)
	}
	_ = admin.Close(ctx)

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("TEST_DATABASE_URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatalf("открытие хранилища: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		t.Fatalf("миграции: %v", err)
	}
	t.Cleanup(func() {
		st.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		admin, err := pgx.Connect(ctx, base)
		if err != nil {
			return
		}
		defer admin.Close(ctx)
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	})
	return st
}

func sanitize(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '_'
	}, name)
}
