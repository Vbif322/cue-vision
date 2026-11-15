// Package auth — пароли, токены сессий и ограничение попыток входа.
// Хранилища не знает: проверяет и выдаёт значения, а хранит их store.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Пароль хранится как pbkdf2-sha256$<итерации>$<соль>$<ключ>, соль и ключ — base64 без
// дополнения. Число итераций записано в самой строке: его можно поднять, не ломая
// уже выданные пароли. 600 000 — рекомендация OWASP для PBKDF2-HMAC-SHA256.
const (
	scheme     = "pbkdf2-sha256"
	iterations = 600_000
	saltLen    = 16
	keyLen     = 32
)

var b64 = base64.RawStdEncoding

// Пределы пароля. Верхний защищает от многомегабайтного пароля в теле запроса.
const (
	MinPasswordRunes = 8
	MaxPasswordBytes = 256
)

// CheckPassword проверяет пароль на допустимую длину.
func CheckPassword(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordRunes || len(password) > MaxPasswordBytes {
		return fmt.Errorf("пароль: от %d символов и не длиннее %d байт", MinPasswordRunes, MaxPasswordBytes)
	}
	return nil
}

// HashPassword возвращает строку для хранения в базе.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	_, _ = rand.Read(salt) // crypto/rand.Read не возвращает ошибок
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, keyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s$%d$%s$%s", scheme, iterations, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword сверяет пароль с сохранённой строкой. Испорченная строка — несовпадение.
func VerifyPassword(stored, password string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != scheme {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return false
	}
	salt, err1 := b64.DecodeString(parts[2])
	want, err2 := b64.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash — строка, с которой сверяется пароль несуществующего пользователя:
// ответ «нет такого логина» занимает столько же времени, сколько «неверный пароль».
var dummyHash = sync.OnceValue(func() string {
	h, _ := HashPassword("cue-vision: нет такого пользователя")
	return h
})

// BurnTime тратит на проверку столько же, сколько VerifyPassword на настоящем пароле.
func BurnTime(password string) { VerifyPassword(dummyHash(), password) }

var loginRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`)

// NormalizeLogin приводит логин к виду, в котором он хранится: логины не различают регистр.
func NormalizeLogin(login string) string { return strings.ToLower(strings.TrimSpace(login)) }

// ValidLogin проверяет уже нормализованный логин.
func ValidLogin(login string) bool { return loginRe.MatchString(login) }

// LoginRule — правило логина для сообщений об ошибке.
const LoginRule = "логин: латинские буквы, цифры, точка, дефис, подчёркивание; от 2 до 64 символов"

// NewToken выдаёт токен сессии для cookie и его отпечаток для базы. Сам токен в базе
// не хранится: утечка таблицы сессий не даёт войти.
func NewToken() (token string, hash []byte) {
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token)
}

// HashToken — отпечаток токена сессии.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Limiter считает неудачные попытки по ключу и закрывает ключ, когда их больше max
// за окно window. Состояние в памяти процесса: после перезапуска счёт начинается заново.
type Limiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]*limitEntry
}

type limitEntry struct {
	failures int
	reset    time.Time
}

// NewLimiter создаёт ограничитель.
func NewLimiter(maxFailures int, window time.Duration) *Limiter {
	return &Limiter{max: maxFailures, window: window, now: time.Now, entries: map[string]*limitEntry{}}
}

// Allow сообщает, можно ли пробовать; если нет — через сколько.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		return true, 0
	}
	now := l.now()
	if !now.Before(e.reset) {
		delete(l.entries, key)
		return true, 0
	}
	if e.failures >= l.max {
		return false, e.reset.Sub(now)
	}
	return true, 0
}

// Fail записывает неудачную попытку.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.entries) > 10_000 {
		for k, e := range l.entries {
			if !now.Before(e.reset) {
				delete(l.entries, k)
			}
		}
	}
	e, ok := l.entries[key]
	if !ok || !now.Before(e.reset) {
		e = &limitEntry{reset: now.Add(l.window)}
		l.entries[key] = e
	}
	e.failures++
}

// Reset забывает неудачи по ключу — после успешного входа.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}
