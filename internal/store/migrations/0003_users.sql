-- Пользователи сервиса (персонал клуба) и их сессии входа.
--
-- users         — учётная запись: логин, пароль (PBKDF2, см. internal/auth), роль.
--                 admin заводит пользователей, staff — всё остальное.
-- auth_sessions — вход в браузере: в cookie лежит случайный токен, здесь — его
--                 SHA-256, сам токен сервис не хранит.

CREATE TABLE users (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    login         text        NOT NULL UNIQUE CHECK (login ~ '^[a-z0-9][a-z0-9._-]{1,63}$'),
    password_hash text        NOT NULL,
    role          text        NOT NULL CHECK (role IN ('admin', 'staff')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE auth_sessions (
    token_hash bytea       PRIMARY KEY,
    user_id    bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    user_agent text
);

CREATE INDEX auth_sessions_user_idx ON auth_sessions (user_id);
CREATE INDEX auth_sessions_expires_idx ON auth_sessions (expires_at);
