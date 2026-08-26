package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ops-deploy-platform/internal/access"
	"ops-deploy-platform/internal/appconfig"
)

type testUserStore struct {
	user access.User
	err  error
}

func (s *testUserStore) GetUser(context.Context, string) (access.User, error) {
	return s.user, s.err
}

func testService(t *testing.T, maxAttempts int) *Service {
	t.Helper()
	hash, err := HashPassword([]byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(appconfig.SecurityConfig{
		AdminUsername: "admin", PasswordHashEnv: "TEST_HASH", SessionTTL: time.Hour,
		LoginMaxAttempts: maxAttempts, LoginWindow: time.Minute, LoginLockout: time.Minute,
	}, hash)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestPasswordHashAndVerification(t *testing.T) {
	encoded, err := HashPassword([]byte("a strong test password"))
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(encoded, []byte("a strong test password")) {
		t.Fatal("correct password did not verify")
	}
	if VerifyPassword(encoded, []byte("wrong password")) {
		t.Fatal("wrong password verified")
	}
}

func TestLoginCookieUsesEightHourSessionTTL(t *testing.T) {
	hash, err := HashPassword([]byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(appconfig.SecurityConfig{
		AdminUsername: "admin", PasswordHashEnv: "TEST_HASH", SessionTTL: 8 * time.Hour,
		LoginMaxAttempts: 5, LoginWindow: time.Minute, LoginLockout: time.Minute,
	}, hash)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	response := httptest.NewRecorder()
	before := time.Now().UTC()
	session, err := service.Login(response, request, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	if cookies[0].MaxAge != int((8 * time.Hour).Seconds()) {
		t.Fatalf("cookie MaxAge = %d, want %d", cookies[0].MaxAge, int((8 * time.Hour).Seconds()))
	}
	if session.ExpiresAt.Before(before.Add(8*time.Hour-time.Second)) || session.ExpiresAt.After(before.Add(8*time.Hour+time.Second)) {
		t.Fatalf("session expiry = %s, want about eight hours", session.ExpiresAt)
	}
}

func TestLoginFailuresDoNotLockAccount(t *testing.T) {
	service := testService(t, 2)
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	for i := 0; i < 5; i++ {
		if _, err := service.Login(httptest.NewRecorder(), request, "admin", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failure %d = %v", i, err)
		}
	}
	if _, err := service.Login(httptest.NewRecorder(), request, "admin", "correct horse battery staple"); err != nil {
		t.Fatalf("correct credentials must work after repeated failures: %v", err)
	}
}

func TestReauthenticationHasIndependentRateLimit(t *testing.T) {
	service := testService(t, 2)
	request := httptest.NewRequest(http.MethodPost, "/api/jobs", nil)
	request.RemoteAddr = "192.0.2.2:1234"
	for i := 0; i < 2; i++ {
		if err := service.ReauthenticateRequest(request, "admin", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failure %d = %v", i, err)
		}
	}
	if err := service.ReauthenticateRequest(request, "admin", "correct horse battery staple"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected rate limit, got %v", err)
	}
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	loginRequest.RemoteAddr = request.RemoteAddr
	if _, err := service.Login(httptest.NewRecorder(), loginRequest, "admin", "correct horse battery staple"); err != nil {
		t.Fatalf("reauthentication failures must not lock login scope: %v", err)
	}
}

func TestDatabaseUserSessionInvalidatesAfterAccountUpdate(t *testing.T) {
	hash, err := HashPassword([]byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	updatedAt := time.Now().UTC().Truncate(time.Microsecond)
	users := &testUserStore{user: access.User{
		Username: "operator", DisplayName: "Operator", PasswordHash: hash,
		IsAdmin: true, Active: true, UpdatedAt: updatedAt,
	}}
	service, err := NewServiceWithUsers(appconfig.SecurityConfig{
		AdminUsername: "admin", PasswordHashEnv: "TEST_HASH", SessionTTL: time.Hour,
		LoginMaxAttempts: 5, LoginWindow: time.Minute, LoginLockout: time.Minute,
	}, hash, users)
	if err != nil {
		t.Fatal(err)
	}

	loginRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	loginResponse := httptest.NewRecorder()
	if _, err := service.Login(loginResponse, loginRequest, "operator", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d", len(cookies))
	}
	authenticatedRequest := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	authenticatedRequest.AddCookie(cookies[0])
	if _, err := service.Authenticate(authenticatedRequest); err != nil {
		t.Fatal(err)
	}

	users.user.IsAdmin = false
	users.user.UpdatedAt = updatedAt.Add(time.Microsecond)
	if _, err := service.Authenticate(authenticatedRequest); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("updated user session should be invalidated, got %v", err)
	}
}

func TestCanceledUserLookupDoesNotDeleteSession(t *testing.T) {
	hash, err := HashPassword([]byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	updatedAt := time.Now().UTC().Truncate(time.Microsecond)
	users := &testUserStore{user: access.User{
		Username: "operator", DisplayName: "Operator", PasswordHash: hash,
		IsAdmin: true, Active: true, UpdatedAt: updatedAt,
	}}
	service, err := NewServiceWithUsers(appconfig.SecurityConfig{
		AdminUsername: "admin", PasswordHashEnv: "TEST_HASH", SessionTTL: time.Hour,
		LoginMaxAttempts: 5, LoginWindow: time.Minute, LoginLockout: time.Minute,
	}, hash, users)
	if err != nil {
		t.Fatal(err)
	}

	loginRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	loginResponse := httptest.NewRecorder()
	if _, err := service.Login(loginResponse, loginRequest, "operator", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	cookie := loginResponse.Result().Cookies()[0]
	authenticatedRequest := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	authenticatedRequest.AddCookie(cookie)

	users.err = context.Canceled
	if _, err := service.Authenticate(authenticatedRequest); !errors.Is(err, context.Canceled) {
		t.Fatalf("temporary lookup error = %v", err)
	}
	users.err = nil
	if _, err := service.Authenticate(authenticatedRequest); err != nil {
		t.Fatalf("temporary lookup error deleted a valid session: %v", err)
	}
}
