package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"ops-deploy-platform/internal/access"
	"ops-deploy-platform/internal/appconfig"
)

const CookieName = "ops_deploy_session"

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrRateLimited        = errors.New("too many login attempts; try again later")
	ErrUnauthenticated    = errors.New("authentication required")
	ErrInvalidCSRF        = errors.New("invalid CSRF token")
)

type Session struct {
	Username            string                    `json:"username"`
	DisplayName         string                    `json:"display_name"`
	IsAdmin             bool                      `json:"is_admin"`
	PlatformPermissions access.PlatformPermission `json:"platform_permissions"`
	CSRFToken           string                    `json:"csrf_token"`
	ExpiresAt           time.Time                 `json:"expires_at"`
}

type storedSession struct {
	Session
	UserUpdatedAt time.Time
}

type loginAttempt struct {
	Count        int
	WindowStart  time.Time
	BlockedUntil time.Time
}

type Service struct {
	username     string
	passwordHash string
	users        UserStore
	ttl          time.Duration
	cookieSecure bool
	cookieName   string
	maxAttempts  int
	window       time.Duration
	lockout      time.Duration

	mu          sync.Mutex
	sessions    map[[32]byte]storedSession
	attempts    map[string]loginAttempt
	now         func() time.Time
	verifySlots chan struct{}
}

type UserStore interface {
	GetUser(context.Context, string) (access.User, error)
}

func NewService(config appconfig.SecurityConfig, encodedPasswordHash string) (*Service, error) {
	return NewServiceWithUsers(config, encodedPasswordHash, nil)
}

func NewServiceWithUsers(config appconfig.SecurityConfig, encodedPasswordHash string, users UserStore) (*Service, error) {
	if strings.TrimSpace(encodedPasswordHash) == "" {
		return nil, fmt.Errorf("%s is not set", config.PasswordHashEnv)
	}
	if _, err := parseHash(encodedPasswordHash); err != nil {
		return nil, fmt.Errorf("invalid password hash in %s: %w", config.PasswordHashEnv, err)
	}
	cookieName := strings.TrimSpace(config.SessionCookieName)
	if cookieName == "" {
		cookieName = CookieName
	}
	return &Service{
		username:     config.AdminUsername,
		passwordHash: encodedPasswordHash,
		users:        users,
		ttl:          config.SessionTTL,
		cookieSecure: config.CookieSecure,
		cookieName:   cookieName,
		maxAttempts:  config.LoginMaxAttempts,
		window:       config.LoginWindow,
		lockout:      config.LoginLockout,
		sessions:     make(map[[32]byte]storedSession),
		attempts:     make(map[string]loginAttempt),
		now:          time.Now,
		verifySlots:  make(chan struct{}, 4),
	}, nil
}

func (s *Service) Login(w http.ResponseWriter, r *http.Request, username, password string) (Session, error) {
	now := s.now().UTC()
	// Interactive login must not lock an operator out after several failed
	// attempts. Password verification is still concurrency-bounded, but excess
	// requests wait for a verification slot instead of returning HTTP 429.
	if !s.acquireVerificationContext(r.Context()) {
		return Session{}, r.Context().Err()
	}
	defer s.releaseVerification()

	credentialHash := s.passwordHash
	displayName := s.username
	isAdmin := true
	platformPermissions := access.FullPlatformPermission()
	var userUpdatedAt time.Time
	validUser := subtle.ConstantTimeCompare([]byte(username), []byte(s.username)) == 1
	canonicalUsername := s.username
	if s.users != nil {
		lookupCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		user, err := s.users.GetUser(lookupCtx, strings.TrimSpace(strings.ToLower(username)))
		cancel()
		if err == nil {
			canonicalUsername = user.Username
			displayName = user.DisplayName
			credentialHash = user.PasswordHash
			isAdmin = user.IsAdmin
			platformPermissions = user.PlatformPermissions
			if user.IsAdmin {
				platformPermissions = access.FullPlatformPermission()
			}
			validUser = user.Active
			userUpdatedAt = user.UpdatedAt
		} else {
			validUser = false
		}
	}
	validPassword := len(username) <= 64 && len(password) <= 256 && VerifyPassword(credentialHash, []byte(password))
	if !validUser || !validPassword {
		return Session{}, ErrInvalidCredentials
	}

	rawToken, err := randomToken(32)
	if err != nil {
		return Session{}, err
	}
	csrfToken, err := randomToken(32)
	if err != nil {
		return Session{}, err
	}
	session := Session{
		Username: canonicalUsername, DisplayName: displayName, IsAdmin: isAdmin,
		PlatformPermissions: platformPermissions,
		CSRFToken:           csrfToken, ExpiresAt: now.Add(s.ttl),
	}
	tokenHash := sha256.Sum256([]byte(rawToken))
	s.mu.Lock()
	s.pruneLocked(now)
	s.limitUserSessionsLocked(canonicalUsername, 20)
	s.limitTotalSessionsLocked(10000)
	s.sessions[tokenHash] = storedSession{Session: session, UserUpdatedAt: userUpdatedAt}
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure is mandatory off-loopback and configurable only for loopback HTTP development.
		Name: s.cookieName, Value: rawToken, Path: "/", HttpOnly: true,
		Secure: s.cookieSecure, SameSite: http.SameSiteStrictMode,
		Expires: session.ExpiresAt, MaxAge: int(s.ttl.Seconds()),
	})
	return session, nil
}

func (s *Service) Authenticate(r *http.Request) (Session, error) {
	cookie, err := r.Cookie(s.cookieName)
	if err != nil || cookie.Value == "" {
		return Session{}, ErrUnauthenticated
	}
	tokenHash := sha256.Sum256([]byte(cookie.Value))
	now := s.now().UTC()
	s.mu.Lock()
	stored, ok := s.sessions[tokenHash]
	if !ok || !stored.ExpiresAt.After(now) {
		delete(s.sessions, tokenHash)
		s.mu.Unlock()
		return Session{}, ErrUnauthenticated
	}
	s.mu.Unlock()

	if s.users != nil {
		lookupCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		user, lookupErr := s.users.GetUser(lookupCtx, stored.Username)
		cancel()
		if lookupErr != nil {
			// Request cancellation, a database timeout, or another temporary
			// datastore failure is not evidence that the account or session was
			// revoked. Keep the token so a browser refresh/navigation cannot log
			// the user out merely by aborting an in-flight API request.
			if !errors.Is(lookupErr, os.ErrNotExist) {
				return Session{}, fmt.Errorf("load session user: %w", lookupErr)
			}
			s.deleteSession(tokenHash)
			return Session{}, ErrUnauthenticated
		}
		if !user.Active || user.UpdatedAt.After(stored.UserUpdatedAt) {
			s.deleteSession(tokenHash)
			return Session{}, ErrUnauthenticated
		}
		stored.DisplayName = user.DisplayName
		stored.IsAdmin = user.IsAdmin
		stored.PlatformPermissions = user.PlatformPermissions
		if user.IsAdmin {
			stored.PlatformPermissions = access.FullPlatformPermission()
		}
		s.mu.Lock()
		if _, exists := s.sessions[tokenHash]; exists {
			s.sessions[tokenHash] = stored
		}
		s.mu.Unlock()
	}
	return stored.Session, nil
}

func (s *Service) deleteSession(tokenHash [32]byte) {
	s.mu.Lock()
	delete(s.sessions, tokenHash)
	s.mu.Unlock()
}

// RefreshCurrentSession keeps the caller signed in after a profile-only update
// while preserving the normal updated_at based invalidation for all other sessions.
func (s *Service) RefreshCurrentSession(r *http.Request, user access.User) (Session, error) {
	cookie, err := r.Cookie(s.cookieName)
	if err != nil || cookie.Value == "" {
		return Session{}, ErrUnauthenticated
	}
	tokenHash := sha256.Sum256([]byte(cookie.Value))
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.sessions[tokenHash]
	if !ok || stored.Username != user.Username || !stored.ExpiresAt.After(s.now().UTC()) {
		return Session{}, ErrUnauthenticated
	}
	stored.DisplayName = user.DisplayName
	stored.IsAdmin = user.IsAdmin
	stored.PlatformPermissions = user.PlatformPermissions
	if user.IsAdmin {
		stored.PlatformPermissions = access.FullPlatformPermission()
	}
	stored.UserUpdatedAt = user.UpdatedAt
	s.sessions[tokenHash] = stored
	return stored.Session, nil
}

// Reauthenticate verifies the current user's password without issuing a new
// session. It is used for short-lived access to production credentials.
func (s *Service) Reauthenticate(ctx context.Context, username, password string) error {
	return s.verifyCurrentPassword(ctx, username, password)
}

// ReauthenticateRequest adds an independent client/user lockout to sensitive
// operations, so a stolen session cannot be used to brute-force a password.
func (s *Service) ReauthenticateRequest(r *http.Request, username, password string) error {
	now := s.now().UTC()
	key := "reauth:" + clientAddress(r) + ":" + strings.ToLower(strings.TrimSpace(username))
	s.mu.Lock()
	attempt, blocked := s.attempts[key]
	blocked = blocked && attempt.BlockedUntil.After(now)
	s.mu.Unlock()
	if blocked {
		return ErrRateLimited
	}
	if !s.acquireVerification() {
		return ErrRateLimited
	}
	defer s.releaseVerification()
	if len(password) > 256 {
		s.recordFailure(key, now)
		return ErrInvalidCredentials
	}
	if err := s.verifyCurrentPassword(r.Context(), username, password); err != nil {
		s.recordFailure(key, now)
		return err
	}
	s.mu.Lock()
	delete(s.attempts, key)
	s.mu.Unlock()
	return nil
}

func (s *Service) acquireVerification() bool {
	select {
	case s.verifySlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Service) acquireVerificationContext(ctx context.Context) bool {
	select {
	case s.verifySlots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Service) releaseVerification() { <-s.verifySlots }

func (s *Service) limitUserSessionsLocked(username string, maximum int) {
	for {
		count := 0
		var oldestHash [32]byte
		var oldest time.Time
		for hash, session := range s.sessions {
			if session.Username != username {
				continue
			}
			count++
			if oldest.IsZero() || session.ExpiresAt.Before(oldest) {
				oldest, oldestHash = session.ExpiresAt, hash
			}
		}
		if count < maximum {
			return
		}
		delete(s.sessions, oldestHash)
	}
}

func (s *Service) limitTotalSessionsLocked(maximum int) {
	for len(s.sessions) >= maximum {
		var oldestHash [32]byte
		var oldest time.Time
		for hash, session := range s.sessions {
			if oldest.IsZero() || session.ExpiresAt.Before(oldest) {
				oldest, oldestHash = session.ExpiresAt, hash
			}
		}
		delete(s.sessions, oldestHash)
	}
}

func (s *Service) verifyCurrentPassword(ctx context.Context, username, password string) error {
	credentialHash := s.passwordHash
	validUser := subtle.ConstantTimeCompare([]byte(username), []byte(s.username)) == 1
	if s.users != nil {
		lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		user, err := s.users.GetUser(lookupCtx, strings.TrimSpace(strings.ToLower(username)))
		cancel()
		if err != nil || !user.Active {
			validUser = false
		} else {
			credentialHash = user.PasswordHash
			validUser = true
		}
	}
	if !validUser || !VerifyPassword(credentialHash, []byte(password)) {
		return ErrInvalidCredentials
	}
	return nil
}

func (s *Service) ValidateCSRF(r *http.Request, session Session) error {
	provided := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
	if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(session.CSRFToken)) != 1 {
		return ErrInvalidCSRF
	}
	return nil
}

func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(s.cookieName); err == nil {
		tokenHash := sha256.Sum256([]byte(cookie.Value))
		s.mu.Lock()
		delete(s.sessions, tokenHash)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- clearing must use the same validated Secure setting as the original cookie.
		Name: s.cookieName, Value: "", Path: "/", HttpOnly: true,
		Secure: s.cookieSecure, SameSite: http.SameSiteStrictMode,
		Expires: time.Unix(1, 0), MaxAge: -1,
	})
}

func (s *Service) recordFailure(client string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt := s.attempts[client]
	if attempt.WindowStart.IsZero() || now.Sub(attempt.WindowStart) > s.window {
		attempt = loginAttempt{WindowStart: now}
	}
	attempt.Count++
	if attempt.Count >= s.maxAttempts {
		attempt.BlockedUntil = now.Add(s.lockout)
	}
	s.attempts[client] = attempt
	s.pruneLocked(now)
}

func (s *Service) pruneLocked(now time.Time) {
	for key, session := range s.sessions {
		if !session.ExpiresAt.After(now) {
			delete(s.sessions, key)
		}
	}
	for key, attempt := range s.attempts {
		if !attempt.BlockedUntil.After(now) && now.Sub(attempt.WindowStart) > s.window+s.lockout {
			delete(s.attempts, key)
		}
	}
}

type contextKey struct{}

func WithSession(ctx context.Context, session Session) context.Context {
	return context.WithValue(ctx, contextKey{}, session)
}

func SessionFromContext(ctx context.Context) (Session, bool) {
	session, ok := ctx.Value(contextKey{}).(Session)
	return session, ok
}

type passwordParams struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
	Salt        []byte
	Hash        []byte
}

func HashPassword(password []byte) (string, error) {
	params := passwordParams{Memory: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32}
	params.Salt = make([]byte, params.SaltLength)
	if _, err := rand.Read(params.Salt); err != nil {
		return "", err
	}
	params.Hash = argon2.IDKey(password, params.Salt, params.Iterations, params.Memory, params.Parallelism, params.KeyLength)
	encoding := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, params.Memory, params.Iterations, params.Parallelism,
		encoding.EncodeToString(params.Salt), encoding.EncodeToString(params.Hash)), nil
}

func VerifyPassword(encoded string, password []byte) bool {
	params, err := parseHash(encoded)
	if err != nil {
		// Run a fixed-cost derivation to reduce malformed-hash timing differences.
		_ = argon2.IDKey(password, make([]byte, 16), 3, 64*1024, 2, 32)
		return false
	}
	candidate := argon2.IDKey(password, params.Salt, params.Iterations, params.Memory, params.Parallelism, 32)
	return subtle.ConstantTimeCompare(candidate, params.Hash) == 1
}

func parseHash(encoded string) (passwordParams, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return passwordParams{}, errors.New("unsupported Argon2id encoding")
	}
	var params passwordParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.Memory, &params.Iterations, &params.Parallelism); err != nil {
		return passwordParams{}, errors.New("invalid Argon2id parameters")
	}
	if params.Memory < 32*1024 || params.Memory > 1024*1024 || params.Iterations < 2 || params.Iterations > 10 || params.Parallelism < 1 || params.Parallelism > 16 {
		return passwordParams{}, errors.New("Argon2id parameters outside accepted bounds")
	}
	var err error
	params.Salt, err = base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(params.Salt) < 16 || len(params.Salt) > 64 {
		return passwordParams{}, errors.New("invalid Argon2id salt")
	}
	params.Hash, err = base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(params.Hash) != 32 {
		return passwordParams{}, errors.New("invalid Argon2id hash")
	}
	return params, nil
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
