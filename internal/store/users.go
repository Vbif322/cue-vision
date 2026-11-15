package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// Роли пользователей.
const (
	RoleAdmin = "admin" // заводит пользователей и меняет их роли и пароли
	RoleStaff = "staff" // всё, кроме управления пользователями
)

// ValidRole проверяет роль.
func ValidRole(role string) bool { return role == RoleAdmin || role == RoleStaff }

// User — учётная запись персонала. Пароль наружу не отдаётся.
type User struct {
	ID        int64     `json:"id" db:"id"`
	Login     string    `json:"login" db:"login"`
	Role      string    `json:"role" db:"role"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

const userColumns = `id, login, role, created_at, updated_at`

// CreateUser заводит пользователя. Логин и хеш пароля проверяет вызывающий.
func (s *Store) CreateUser(ctx context.Context, login, passwordHash, role string) (User, error) {
	u, err := one[User](s.pool.Query(ctx,
		`INSERT INTO users (login, password_hash, role) VALUES ($1, $2, $3) RETURNING `+userColumns,
		login, passwordHash, role))
	if isUniqueViolation(err) {
		return User{}, &ConflictError{Msg: "пользователь с таким логином уже есть"}
	}
	return u, err
}

// BootstrapAdmin заводит администратора, только если пользователей ещё нет.
// Возвращает false, если кто-то уже есть: пароль из настроек тогда не применяется,
// чтобы перезапуск сервиса не откатывал сменённый пароль.
func (s *Store) BootstrapAdmin(ctx context.Context, login, passwordHash string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO users (login, password_hash, role)
		 SELECT $1, $2, 'admin' WHERE NOT EXISTS (SELECT 1 FROM users)`, login, passwordHash)
	if isUniqueViolation(err) {
		// Другой экземпляр сервиса успел первым.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// CountUsers — число пользователей.
func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// ListUsers возвращает пользователей по логину.
func (s *Store) ListUsers(ctx context.Context, page Page) ([]User, error) {
	return many[User](s.pool.Query(ctx,
		`SELECT `+userColumns+` FROM users ORDER BY login LIMIT $1 OFFSET $2`, page.Limit, page.Offset))
}

// GetUser возвращает пользователя по идентификатору.
func (s *Store) GetUser(ctx context.Context, id int64) (User, error) {
	return one[User](s.pool.Query(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

// UserCredentials возвращает пользователя и хеш его пароля по логину.
func (s *Store) UserCredentials(ctx context.Context, login string) (User, string, error) {
	var (
		u    User
		hash string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT id, login, role, created_at, updated_at, password_hash FROM users WHERE login = $1`, login).
		Scan(&u.ID, &u.Login, &u.Role, &u.CreatedAt, &u.UpdatedAt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", ErrNotFound
	}
	return u, hash, err
}

// PasswordHash возвращает хеш пароля пользователя.
func (s *Store) PasswordHash(ctx context.Context, id int64) (string, error) {
	var hash string
	err := s.pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, id).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return hash, err
}

// UserPatch — изменения пользователя; nil — поле не меняется.
type UserPatch struct {
	Role         *string
	PasswordHash *string
	// KeepSession — сессия, которую смена пароля не закрывает (та, из которой его
	// меняют). Остальные сессии пользователя закрываются.
	KeepSession []byte
}

// UpdateUser меняет роль и пароль. Последнего администратора понизить нельзя.
func (s *Store) UpdateUser(ctx context.Context, id int64, p UserPatch) (User, error) {
	var u User
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if p.Role != nil && *p.Role != RoleAdmin {
			if err := guardLastAdmin(ctx, tx, id, "нельзя снять роль admin с последнего администратора"); err != nil {
				return err
			}
		}
		var err error
		u, err = one[User](tx.Query(ctx,
			`UPDATE users SET role = COALESCE($2, role), password_hash = COALESCE($3, password_hash),
			        updated_at = now()
			  WHERE id = $1 RETURNING `+userColumns, id, p.Role, p.PasswordHash))
		if err != nil {
			return err
		}
		if p.PasswordHash != nil {
			_, err = tx.Exec(ctx,
				`DELETE FROM auth_sessions WHERE user_id = $1 AND token_hash IS DISTINCT FROM $2`, id, p.KeepSession)
		}
		return err
	})
	return u, err
}

// DeleteUser удаляет пользователя вместе с его сессиями. Последнего администратора
// удалить нельзя.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := guardLastAdmin(ctx, tx, id, "нельзя удалить последнего администратора"); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return err
	})
}

// guardLastAdmin не даёт оставить сервис без администратора. Строки администраторов
// блокируются: два одновременных понижения разных админов не проскочат оба.
func guardLastAdmin(ctx context.Context, tx pgx.Tx, id int64, msg string) error {
	rows, err := tx.Query(ctx, `SELECT id FROM users WHERE role = 'admin' FOR UPDATE`)
	if err != nil {
		return err
	}
	admins, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	if len(admins) == 1 && slices.Contains(admins, id) {
		return &ConflictError{Msg: msg}
	}
	return nil
}

// --- сессии входа ---

// CreateAuthSession открывает сессию и заодно убирает истёкшие.
func (s *Store) CreateAuthSession(ctx context.Context, userID int64, tokenHash []byte, expiresAt time.Time, userAgent string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM auth_sessions WHERE expires_at < now()`); err != nil {
		return err
	}
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO auth_sessions (token_hash, user_id, expires_at, user_agent) VALUES ($1, $2, $3, NULLIF($4, ''))`,
		tokenHash, userID, expiresAt, userAgent)
	return err
}

// AuthSessionUser возвращает владельца действующей сессии и срок её окончания.
func (s *Store) AuthSessionUser(ctx context.Context, tokenHash []byte) (User, time.Time, error) {
	var (
		u       User
		expires time.Time
	)
	err := s.pool.QueryRow(ctx,
		`SELECT u.id, u.login, u.role, u.created_at, u.updated_at, a.expires_at
		   FROM auth_sessions a JOIN users u ON u.id = a.user_id
		  WHERE a.token_hash = $1 AND a.expires_at > now()`, tokenHash).
		Scan(&u.ID, &u.Login, &u.Role, &u.CreatedAt, &u.UpdatedAt, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, time.Time{}, ErrNotFound
	}
	return u, expires, err
}

// ExtendAuthSession переносит окончание сессии.
func (s *Store) ExtendAuthSession(ctx context.Context, tokenHash []byte, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE auth_sessions SET expires_at = $2 WHERE token_hash = $1`, tokenHash, expiresAt)
	return err
}

// DeleteAuthSession закрывает сессию (выход).
func (s *Store) DeleteAuthSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM auth_sessions WHERE token_hash = $1`, tokenHash)
	return err
}
