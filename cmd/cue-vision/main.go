// Команда cue-vision — сервис приёма событий детектора snooker_CV и статистики тренировок.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata" // часовые пояса для разбивки статистики по дням в образе без tzdata

	"github.com/Vbif322/cue-vision/internal/api"
	"github.com/Vbif322/cue-vision/internal/auth"
	"github.com/Vbif322/cue-vision/internal/config"
	"github.com/Vbif322/cue-vision/internal/detector"
	"github.com/Vbif322/cue-vision/internal/ingest"
	"github.com/Vbif322/cue-vision/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "cue-vision:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := openStore(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return fmt.Errorf("база данных: %w", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("миграции: %w", err)
	}
	if err := bootstrapAdmin(ctx, st, cfg, log); err != nil {
		return fmt.Errorf("первый администратор: %w", err)
	}

	processor := ingest.New(st.Pool(), ingest.Options{
		AutoTraining: cfg.AutoTraining,
		SkipTypes:    cfg.SkipTypes,
	}, log)
	detectors := detector.New(cfg.Detectors, processor, cfg.IdleTimeout, log)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.New(cfg, st, processor, detectors, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		detectors.Run(ctx)
	}()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	log.Info("сервис запущен", "addr", cfg.HTTPAddr, "detectors", len(cfg.Detectors),
		"auto_training", cfg.AutoTraining)

	select {
	case err := <-serveErr:
		stop()
		wg.Wait()
		return err
	case <-ctx.Done():
	}

	log.Info("остановка")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	wg.Wait()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// bootstrapAdmin заводит администратора из ADMIN_LOGIN и ADMIN_PASSWORD, пока в базе
// нет ни одного пользователя. Дальше пароль из настроек не действует: его меняют через API.
func bootstrapAdmin(ctx context.Context, st *store.Store, cfg config.Config, log *slog.Logger) error {
	if cfg.AdminLogin != "" {
		login := auth.NormalizeLogin(cfg.AdminLogin)
		if !auth.ValidLogin(login) {
			return fmt.Errorf("ADMIN_LOGIN: %s", auth.LoginRule)
		}
		if err := auth.CheckPassword(cfg.AdminPassword); err != nil {
			return fmt.Errorf("ADMIN_PASSWORD: %w", err)
		}
		hash, err := auth.HashPassword(cfg.AdminPassword)
		if err != nil {
			return err
		}
		created, err := st.BootstrapAdmin(ctx, login, hash)
		if err != nil {
			return err
		}
		if created {
			log.Info("заведён первый администратор", "login", login)
		}
	}
	n, err := st.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n == 0 && cfg.APIToken == "" {
		log.Warn("пользователей нет и API_TOKEN не задан: в API никто не войдёт; задайте ADMIN_LOGIN и ADMIN_PASSWORD")
	}
	return nil
}

// openStore ждёт базу: в compose сервис нередко стартует раньше, чем PostgreSQL
// начинает принимать соединения.
func openStore(ctx context.Context, url string, log *slog.Logger) (*store.Store, error) {
	const attempts = 30
	var lastErr error
	for i := 1; i <= attempts; i++ {
		st, err := store.Open(ctx, url)
		if err == nil {
			return st, nil
		}
		lastErr = err
		log.Warn("база недоступна, повтор", "attempt", i, "error", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil, lastErr
}
