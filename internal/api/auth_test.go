package api_test

import (
	"net/http"
	"net/http/cookiejar"
	"testing"
)

type userResp struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Role  string `json:"role"`
}

type meResp struct {
	User *userResp `json:"user"`
	Via  string    `json:"via"`
}

// browser — клиент без токена, который хранит cookie, как браузер.
func browser(t *testing.T, base string) *client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &client{t: t, base: base, hc: &http.Client{Jar: jar}}
}

func creds(login, password string) map[string]string {
	return map[string]string{"login": login, "password": password}
}

func TestLoginAndUsers(t *testing.T) {
	root := newServer(t, "")

	var me meResp
	root.expect(http.StatusOK, "GET", "/api/v1/auth/me", nil, &me)
	if me.Via != "token" || me.User != nil {
		t.Fatalf("вход по токену: %+v", me)
	}

	// Заводит пользователей токен: у него права администратора.
	var boss, marker userResp
	root.expect(http.StatusCreated, "POST", "/api/v1/users",
		map[string]string{"login": " Boss ", "password": "boss-password", "role": "admin"}, &boss)
	if boss.Login != "boss" || boss.Role != "admin" {
		t.Fatalf("администратор: %+v", boss)
	}
	root.expect(http.StatusCreated, "POST", "/api/v1/users", creds("marker", "marker-password"), &marker)
	if marker.Role != "staff" {
		t.Fatalf("роль по умолчанию: %+v", marker)
	}
	root.expect(http.StatusConflict, "POST", "/api/v1/users", creds("BOSS", "another-password"), nil)
	root.expect(http.StatusBadRequest, "POST", "/api/v1/users", creds("short", "1234567"), nil)
	root.expect(http.StatusBadRequest, "POST", "/api/v1/users", creds("два слова", "long-enough"), nil)
	root.expect(http.StatusBadRequest, "POST", "/api/v1/users",
		map[string]string{"login": "x1", "password": "long-enough", "role": "root"}, nil)

	// Вход.
	b := browser(t, root.base)
	b.expect(http.StatusUnauthorized, "GET", "/api/v1/sessions", nil, nil)
	b.expect(http.StatusUnauthorized, "POST", "/api/v1/auth/login", creds("boss", "wrong-password"), nil)
	b.expect(http.StatusUnauthorized, "POST", "/api/v1/auth/login", creds("nobody", "boss-password"), nil)
	b.expect(http.StatusBadRequest, "POST", "/api/v1/auth/login", creds("boss", ""), nil)
	b.expect(http.StatusOK, "POST", "/api/v1/auth/login", creds("BOSS", "boss-password"), &me)
	if me.Via != "session" || me.User == nil || me.User.ID != boss.ID {
		t.Fatalf("ответ на вход: %+v", me)
	}
	b.expect(http.StatusOK, "GET", "/api/v1/auth/me", nil, &me)
	if me.User == nil || me.User.Login != "boss" {
		t.Fatalf("кто вошёл: %+v", me)
	}
	b.expect(http.StatusOK, "GET", "/api/v1/sessions", nil, nil)
	b.expect(http.StatusOK, "GET", "/api/v1/users", nil, nil)

	// Чужой токен не пускает, даже если есть cookie.
	if got := b.do("GET", "/api/v1/sessions", nil, map[string]string{"Authorization": "Bearer nope"}, nil); got != http.StatusUnauthorized {
		t.Fatalf("неверный токен: %d", got)
	}

	// staff работает с данными, но не с пользователями.
	m := browser(t, root.base)
	m.expect(http.StatusOK, "POST", "/api/v1/auth/login", creds("marker", "marker-password"), nil)
	m.expect(http.StatusOK, "GET", "/api/v1/trainings", nil, nil)
	m.expect(http.StatusForbidden, "GET", "/api/v1/users", nil, nil)
	m.expect(http.StatusForbidden, "POST", "/api/v1/users", creds("intruder", "long-enough"), nil)

	// Изменяющий запрос с чужого сайта отклоняется, хотя cookie браузер приложил.
	crossSite := map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}
	if got := m.do("POST", "/api/v1/players", map[string]string{"name": "X"}, crossSite, nil); got != http.StatusForbidden {
		t.Fatalf("запрос с чужого сайта: %d", got)
	}

	// Без администратора сервис не остаётся.
	b.expect(http.StatusConflict, "PATCH", "/api/v1/users/"+itoa(boss.ID), map[string]string{"role": "staff"}, nil)
	b.expect(http.StatusConflict, "DELETE", "/api/v1/users/"+itoa(boss.ID), nil, nil)
	root.expect(http.StatusConflict, "DELETE", "/api/v1/users/"+itoa(boss.ID), nil, nil)
	root.expect(http.StatusBadRequest, "PATCH", "/api/v1/users/"+itoa(boss.ID), map[string]string{}, nil)

	// Смена своего пароля: эта сессия остаётся, другие закрываются.
	m2 := browser(t, root.base)
	m2.expect(http.StatusOK, "POST", "/api/v1/auth/login", creds("marker", "marker-password"), nil)
	m.expect(http.StatusForbidden, "POST", "/api/v1/auth/password",
		map[string]string{"current_password": "wrong-password", "new_password": "marker-password-2"}, nil)
	m.expect(http.StatusBadRequest, "POST", "/api/v1/auth/password",
		map[string]string{"current_password": "marker-password", "new_password": "short"}, nil)
	m.expect(http.StatusNoContent, "POST", "/api/v1/auth/password",
		map[string]string{"current_password": "marker-password", "new_password": "marker-password-2"}, nil)
	m.expect(http.StatusOK, "GET", "/api/v1/auth/me", nil, nil)
	m2.expect(http.StatusUnauthorized, "GET", "/api/v1/auth/me", nil, nil)
	browser(t, root.base).expect(http.StatusUnauthorized, "POST", "/api/v1/auth/login", creds("marker", "marker-password"), nil)
	browser(t, root.base).expect(http.StatusOK, "POST", "/api/v1/auth/login", creds("marker", "marker-password-2"), nil)
	root.expect(http.StatusBadRequest, "POST", "/api/v1/auth/password",
		map[string]string{"current_password": "x", "new_password": "long-enough"}, nil)

	// Пароль, заданный администратором, закрывает все сессии пользователя.
	b.expect(http.StatusOK, "PATCH", "/api/v1/users/"+itoa(marker.ID), map[string]string{"password": "reset-password"}, nil)
	m.expect(http.StatusUnauthorized, "GET", "/api/v1/auth/me", nil, nil)

	// Повышение до admin: теперь первого администратора можно понизить.
	b.expect(http.StatusOK, "PATCH", "/api/v1/users/"+itoa(marker.ID), map[string]string{"role": "admin"}, nil)
	root.expect(http.StatusOK, "PATCH", "/api/v1/users/"+itoa(boss.ID), map[string]string{"role": "staff"}, nil)
	b.expect(http.StatusForbidden, "GET", "/api/v1/users", nil, nil)

	// Выход.
	b.expect(http.StatusNoContent, "POST", "/api/v1/auth/logout", nil, nil)
	b.expect(http.StatusUnauthorized, "GET", "/api/v1/auth/me", nil, nil)
	b.expect(http.StatusNoContent, "POST", "/api/v1/auth/logout", nil, nil)

	// Удалённый пользователь не входит.
	root.expect(http.StatusNoContent, "DELETE", "/api/v1/users/"+itoa(boss.ID), nil, nil)
	root.expect(http.StatusNotFound, "GET", "/api/v1/users/"+itoa(boss.ID), nil, nil)
	root.expect(http.StatusNotFound, "DELETE", "/api/v1/users/"+itoa(boss.ID), nil, nil)
	browser(t, root.base).expect(http.StatusUnauthorized, "POST", "/api/v1/auth/login", creds("boss", "boss-password"), nil)
}

func TestLoginRateLimit(t *testing.T) {
	root := newServer(t, "")
	root.expect(http.StatusCreated, "POST", "/api/v1/users", creds("player-1", "right-password"), nil)
	b := browser(t, root.base)
	for range 10 {
		b.expect(http.StatusUnauthorized, "POST", "/api/v1/auth/login", creds("player-1", "wrong-password"), nil)
	}
	// Даже верный пароль не принимается, пока окно не прошло.
	b.expect(http.StatusTooManyRequests, "POST", "/api/v1/auth/login", creds("player-1", "right-password"), nil)
}
