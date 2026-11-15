package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Vbif322/cue-vision/internal/auth"
	"github.com/Vbif322/cue-vision/internal/store"
)

// Доступ к /api/v1 — двумя способами:
//
//   - cookie сессии после POST /auth/login — для людей в браузере;
//   - заголовок Authorization: Bearer <API_TOKEN> — для детекторов и скриптов.
//     Токен даёт права администратора.

const sessionCookie = "cv_session"

// Неудачных входов с одного адреса на один логин — не больше loginMaxFailures за loginWindow.
const (
	loginMaxFailures = 10
	loginWindow      = 15 * time.Minute
)

// principal — кто делает запрос.
type principal struct {
	user        *store.User // nil — вход по API_TOKEN
	sessionHash []byte      // отпечаток токена сессии, если вход по cookie
}

func (p principal) isAdmin() bool { return p.user == nil || p.user.Role == store.RoleAdmin }

type principalKey struct{}

func principalFrom(ctx context.Context) (principal, bool) {
	p, ok := ctx.Value(principalKey{}).(principal)
	return p, ok
}

type access int

const (
	accessPublic access = iota
	accessUser
	accessAdmin
)

func unauthorized(w http.ResponseWriter) error {
	w.Header().Set("WWW-Authenticate", `Bearer realm="cue-vision"`)
	return &httpError{status: http.StatusUnauthorized, msg: "нужен вход: POST /api/v1/auth/login или заголовок Authorization: Bearer <токен>"}
}

// guard проверяет доступ и кладёт в контекст того, кто пришёл.
func (s *Server) guard(level access, fn handlerFunc) handlerFunc {
	if level == accessPublic {
		return fn
	}
	return func(w http.ResponseWriter, r *http.Request) error {
		p, err := s.authenticate(w, r)
		if err != nil {
			return err
		}
		if level == accessAdmin && !p.isAdmin() {
			return &httpError{status: http.StatusForbidden, msg: "нужна роль admin"}
		}
		return fn(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	}
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (principal, error) {
	if header := r.Header.Get("Authorization"); header != "" {
		got, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || s.cfg.APIToken == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.APIToken)) != 1 {
			return principal{}, unauthorized(w)
		}
		return principal{}, nil
	}

	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return principal{}, unauthorized(w)
	}
	hash := auth.HashToken(c.Value)
	user, expires, err := s.store.AuthSessionUser(r.Context(), hash)
	if errors.Is(err, store.ErrNotFound) {
		s.clearSessionCookie(w, r)
		return principal{}, unauthorized(w)
	}
	if err != nil {
		return principal{}, err
	}
	// Скользящий срок: сессия, которой пользуются, не истекает. Продлеваем не на
	// каждом запросе, а когда прошла половина срока.
	if now := time.Now(); expires.Sub(now) < s.cfg.SessionTTL/2 {
		expires = now.Add(s.cfg.SessionTTL)
		if err := s.store.ExtendAuthSession(r.Context(), hash, expires); err != nil {
			return principal{}, err
		}
		s.setSessionCookie(w, r, c.Value, expires)
	}
	return principal{user: &user, sessionHash: hash}, nil
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(math.Ceil(time.Until(expires).Seconds())),
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure || r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure || r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

// clientIP — адрес соединения. X-Forwarded-For не учитывается: его может подставить
// кто угодно, а ограничение попыток входа на нём держится.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// --- вход и выход ---

type loginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

type meResponse struct {
	User *store.User `json:"user"`
	// Via — способ входа: session (cookie) или token (API_TOKEN).
	Via string `json:"via"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) error {
	var req loginRequest
	if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}
	login := auth.NormalizeLogin(req.Login)
	if login == "" || req.Password == "" {
		return badRequest("нужны login и password")
	}
	if len(req.Password) > auth.MaxPasswordBytes {
		return badRequest("пароль длиннее %d байт", auth.MaxPasswordBytes)
	}

	key := clientIP(r) + "|" + login
	if ok, wait := s.loginLimiter.Allow(key); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		return &httpError{status: http.StatusTooManyRequests, msg: "слишком много неудачных попыток входа, попробуйте позже"}
	}

	user, hash, err := s.store.UserCredentials(r.Context(), login)
	switch {
	case errors.Is(err, store.ErrNotFound):
		auth.BurnTime(req.Password)
	case err != nil:
		return err
	}
	if err != nil || !auth.VerifyPassword(hash, req.Password) {
		s.loginLimiter.Fail(key)
		s.log.Warn("неудачный вход", "login", login, "ip", clientIP(r))
		return &httpError{status: http.StatusUnauthorized, msg: "неверный логин или пароль"}
	}
	s.loginLimiter.Reset(key)

	token, tokenHash := auth.NewToken()
	expires := time.Now().Add(s.cfg.SessionTTL)
	if err := s.store.CreateAuthSession(r.Context(), user.ID, tokenHash, expires, r.UserAgent()); err != nil {
		return err
	}
	s.setSessionCookie(w, r, token, expires)
	s.log.Info("вход", "login", user.Login, "ip", clientIP(r))
	writeJSON(w, http.StatusOK, meResponse{User: &user, Via: "session"})
	return nil
}

// logout закрывает сессию из cookie. Доступен без входа: протухшая cookie тоже
// должна стираться.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := s.store.DeleteAuthSession(r.Context(), auth.HashToken(c.Value)); err != nil {
			return err
		}
	}
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) error {
	p, _ := principalFrom(r.Context())
	if p.user == nil {
		writeJSON(w, http.StatusOK, meResponse{Via: "token"})
		return nil
	}
	writeJSON(w, http.StatusOK, meResponse{User: p.user, Via: "session"})
	return nil
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// changePassword — смена собственного пароля. Остальные сессии пользователя закрываются.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) error {
	p, _ := principalFrom(r.Context())
	if p.user == nil {
		return badRequest("у входа по токену нет пароля")
	}
	var req changePasswordRequest
	if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}
	if err := auth.CheckPassword(req.NewPassword); err != nil {
		return badRequest("%v", err)
	}

	key := clientIP(r) + "|" + p.user.Login
	if ok, wait := s.loginLimiter.Allow(key); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		return &httpError{status: http.StatusTooManyRequests, msg: "слишком много неудачных попыток, попробуйте позже"}
	}
	current, err := s.store.PasswordHash(r.Context(), p.user.ID)
	if err != nil {
		return err
	}
	if !auth.VerifyPassword(current, req.CurrentPassword) {
		s.loginLimiter.Fail(key)
		return &httpError{status: http.StatusForbidden, msg: "текущий пароль неверен"}
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		return err
	}
	if _, err := s.store.UpdateUser(r.Context(), p.user.ID, store.UserPatch{PasswordHash: &hash, KeepSession: p.sessionHash}); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- пользователи (только admin) ---

type createUserRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) error {
	var req createUserRequest
	if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}
	login := auth.NormalizeLogin(req.Login)
	if !auth.ValidLogin(login) {
		return badRequest(auth.LoginRule)
	}
	if req.Role == "" {
		req.Role = store.RoleStaff
	}
	if !store.ValidRole(req.Role) {
		return badRequest("роль: admin или staff")
	}
	if err := auth.CheckPassword(req.Password); err != nil {
		return badRequest("%v", err)
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return err
	}
	user, err := s.store.CreateUser(r.Context(), login, hash, req.Role)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, user)
	return nil
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) error {
	page, err := pageOf(r)
	if err != nil {
		return err
	}
	items, err := s.store.ListUsers(r.Context(), page)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list[store.User]{Items: items})
	return nil
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	user, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, user)
	return nil
}

type updateUserRequest struct {
	Role     *string `json:"role"`
	Password *string `json:"password"`
}

// updateUser меняет роль или задаёт новый пароль. Новый пароль закрывает все сессии
// пользователя, кроме той, из которой его меняют.
func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var req updateUserRequest
	if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}
	if req.Role == nil && req.Password == nil {
		return badRequest("нечего менять: ожидаются role и/или password")
	}
	patch := store.UserPatch{Role: req.Role}
	if req.Role != nil && !store.ValidRole(*req.Role) {
		return badRequest("роль: admin или staff")
	}
	if req.Password != nil {
		if err := auth.CheckPassword(*req.Password); err != nil {
			return badRequest("%v", err)
		}
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			return err
		}
		patch.PasswordHash = &hash
		if p, _ := principalFrom(r.Context()); p.user != nil && p.user.ID == id {
			patch.KeepSession = p.sessionHash
		}
	}
	user, err := s.store.UpdateUser(r.Context(), id, patch)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, user)
	return nil
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if p, _ := principalFrom(r.Context()); p.user != nil && p.user.ID == id {
		return &httpError{status: http.StatusConflict, msg: "нельзя удалить себя"}
	}
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// crossOrigin отклоняет изменяющие запросы с чужих сайтов: cookie сессии браузер
// приложил бы к ним сам. Запросы без Origin и Sec-Fetch-Site (curl, детектор)
// проходят — у них нет cookie, они входят токеном.
func (s *Server) crossOrigin(next http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "запрос с чужого сайта отклонён"})
	}))
	return cop.Handler(next)
}
