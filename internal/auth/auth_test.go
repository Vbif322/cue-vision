package auth

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestPassword(t *testing.T) {
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha256$600000$") {
		t.Fatalf("формат: %s", hash)
	}
	if !VerifyPassword(hash, "correct horse") {
		t.Fatal("верный пароль не принят")
	}
	if VerifyPassword(hash, "correct horsE") {
		t.Fatal("неверный пароль принят")
	}
	other, _ := HashPassword("correct horse")
	if other == hash {
		t.Fatal("соль не случайна")
	}
	for _, broken := range []string{"", "plain", "bcrypt$1$a$b", "pbkdf2-sha256$x$a$b", "pbkdf2-sha256$1$!!$b", "pbkdf2-sha256$1$aa$"} {
		if VerifyPassword(broken, "correct horse") {
			t.Fatalf("испорченная строка %q принята", broken)
		}
	}
}

func TestCheckPassword(t *testing.T) {
	if CheckPassword("1234567") == nil {
		t.Fatal("7 символов приняты")
	}
	if CheckPassword("пароль12") != nil {
		t.Fatal("8 символов кириллицей не приняты")
	}
	if CheckPassword(strings.Repeat("x", MaxPasswordBytes+1)) == nil {
		t.Fatal("слишком длинный пароль принят")
	}
}

func TestLogin(t *testing.T) {
	if got := NormalizeLogin("  Ivan.Petrov "); got != "ivan.petrov" {
		t.Fatalf("нормализация: %q", got)
	}
	for _, ok := range []string{"ab", "ivan.petrov", "m_1", "0-x"} {
		if !ValidLogin(ok) {
			t.Fatalf("%q отвергнут", ok)
		}
	}
	for _, bad := range []string{"", "a", "-ab", ".ab", "a b", "иван", "Ab", strings.Repeat("a", 65)} {
		if ValidLogin(bad) {
			t.Fatalf("%q принят", bad)
		}
	}
}

func TestToken(t *testing.T) {
	a, ha := NewToken()
	b, _ := NewToken()
	if a == b || len(a) < 40 {
		t.Fatalf("токены: %q %q", a, b)
	}
	if !bytes.Equal(ha, HashToken(a)) {
		t.Fatal("отпечаток не совпал")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(3, time.Minute)
	l.now = func() time.Time { return now }

	for range 3 {
		if ok, _ := l.Allow("k"); !ok {
			t.Fatal("закрыт раньше предела")
		}
		l.Fail("k")
	}
	ok, wait := l.Allow("k")
	if ok || wait != time.Minute {
		t.Fatalf("после предела: %v %v", ok, wait)
	}
	if ok, _ := l.Allow("other"); !ok {
		t.Fatal("закрыт чужой ключ")
	}

	now = now.Add(time.Minute)
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("не открылся после окна")
	}

	l.Fail("k")
	l.Reset("k")
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("Reset не сбросил")
	}
}
