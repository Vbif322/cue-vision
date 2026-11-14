// Package store — доступ к PostgreSQL: миграции, справочники, чтение и статистика.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ErrNotFound — запрошенной записи нет.
var ErrNotFound = errors.New("не найдено")

// ConflictError — операция противоречит текущему состоянию.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// DBTX — общее подмножество пула и транзакции.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store владеет пулом соединений.
type Store struct {
	pool *pgxpool.Pool
}

// Open подключается к базе и проверяет соединение.
func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("разбор DATABASE_URL: %w", err)
	}
	// Без этого pgx отдаёт timestamptz в поясе машины, и ответы API зависели бы
	// от того, где запущен сервис.
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		conn.TypeMap().RegisterType(&pgtype.Type{
			Name:  "timestamptz",
			OID:   pgtype.TimestamptzOID,
			Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC},
		})
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// Pool отдаёт пул для транзакций обработчика потока.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Ping проверяет доступность базы.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Close закрывает пул.
func (s *Store) Close() { s.pool.Close() }

// migrationLock — ключ advisory-блокировки: несколько экземпляров сервиса,
// стартующих одновременно, применяют миграции по очереди.
const migrationLock int64 = 0x6375655f766973

// Migrate применяет ещё не применённые миграции из встроенного каталога.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLock); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLock)
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    integer PRIMARY KEY,
		name       text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}

	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		prefix, _, _ := strings.Cut(base, "_")
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return fmt.Errorf("миграция %s: имя должно начинаться с номера", base)
		}
		var applied bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := migrationsFS.ReadFile(name)
		if err != nil {
			return err
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			// Без аргументов pgx идёт простым протоколом, и файл из нескольких
			// операторов выполняется одним вызовом.
			if _, err := tx.Exec(ctx, string(body)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, version, base)
			return err
		})
		if err != nil {
			return fmt.Errorf("миграция %s: %w", base, err)
		}
	}
	return nil
}

// LockStream сериализует в пределах транзакции всё, что меняет состояние потока:
// приём событий и открытие тренировки не должны переплетаться.
func LockStream(ctx context.Context, tx pgx.Tx, stream string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "cue-vision/stream/"+stream)
	return err
}

// Page — постраничная выборка.
type Page struct {
	Limit  int
	Offset int
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func one[T any](rows pgx.Rows, err error) (T, error) {
	var zero T
	if err != nil {
		return zero, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[T])
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, ErrNotFound
	}
	return v, err
}

func many[T any](rows pgx.Rows, err error) ([]T, error) {
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByName[T])
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []T{}
	}
	return items, nil
}
