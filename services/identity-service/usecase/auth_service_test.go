package usecase

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/repository"
)

type fakeUsers struct{ users map[string]domain.User }

func (r *fakeUsers) Save(_ context.Context, user domain.User) error {
	if _, ok := r.users[user.Email()]; ok {
		return domain.ErrEmailTaken
	}
	r.users[user.Email()] = user
	return nil
}
func (r *fakeUsers) FindByEmail(_ context.Context, email string) (domain.User, error) {
	user, ok := r.users[email]
	if !ok {
		return domain.User{}, domain.ErrUserNotFound
	}
	return user, nil
}
func (r *fakeUsers) FindByID(_ context.Context, id string) (domain.User, error) {
	for _, user := range r.users {
		if user.ID() == id {
			return user, nil
		}
	}
	return domain.User{}, domain.ErrUserNotFound
}

type memorySessions struct {
	mu       sync.Mutex
	sessions map[string]repository.Session
	tokens   map[string]string
	consumed map[string]bool
	revoked  map[string]bool
	next     int
}

func newMemorySessions() *memorySessions {
	return &memorySessions{sessions: map[string]repository.Session{}, tokens: map[string]string{}, consumed: map[string]bool{}, revoked: map[string]bool{}}
}
func (r *memorySessions) Create(_ context.Context, userID string, expiry time.Time, hash []byte) (repository.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	s := repository.Session{ID: fmt.Sprintf("session-%d", r.next), UserID: userID, FamilyID: "family", ExpiresAt: expiry}
	r.sessions[s.ID] = s
	r.tokens[string(hash)] = s.ID
	return s, nil
}
func (r *memorySessions) Rotate(_ context.Context, hash, successor []byte, now time.Time) (repository.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := string(hash)
	id, ok := r.tokens[key]
	if !ok || r.revoked[id] {
		return repository.Session{}, repository.ErrRefreshTokenInvalid
	}
	if r.consumed[key] {
		r.revoked[id] = true
		return repository.Session{}, repository.ErrRefreshTokenReused
	}
	r.consumed[key] = true
	r.tokens[string(successor)] = id
	return r.sessions[id], nil
}
func (r *memorySessions) Validate(_ context.Context, userID, id string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok || s.UserID != userID || r.revoked[id] || !s.ExpiresAt.After(now) {
		return repository.ErrSessionInvalid
	}
	return nil
}
func (r *memorySessions) Logout(_ context.Context, id string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revoked[id] = true
	return nil
}

func newService(t *testing.T) (*AuthService, *auth.Issuer) {
	t.Helper()
	issuer, err := auth.NewIssuer(make([]byte, 32), time.Hour)
	require.NoError(t, err)
	return NewAuthService(&fakeUsers{users: map[string]domain.User{}}, newMemorySessions(), issuer, time.Hour), issuer
}

func TestRegisterCreatesHashOnlySessionAndSessionClaim(t *testing.T) {
	service, issuer := newService(t)
	result, _, err := service.Register(context.Background(), "a@example.com", "password-1234", domain.RoleReader)
	require.NoError(t, err)
	assert.Len(t, result.RefreshToken, 43)
	claims, err := issuer.Validate(result.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, result.SessionID, claims.SessionID)
	assert.NotEqual(t, result.RefreshToken, result.SessionID)
}
func TestRefreshRotatesAndReuseRevokesSession(t *testing.T) {
	service, _ := newService(t)
	result, _, err := service.Register(context.Background(), "a@example.com", "password-1234", domain.RoleReader)
	require.NoError(t, err)
	next, err := service.Refresh(context.Background(), result.RefreshToken)
	require.NoError(t, err)
	assert.NotEqual(t, result.RefreshToken, next.RefreshToken)
	_, err = service.Refresh(context.Background(), result.RefreshToken)
	assert.ErrorIs(t, err, domain.ErrInvalidCredentials)
	assert.ErrorIs(t, service.ValidateSession(context.Background(), "", result.SessionID), domain.ErrInvalidCredentials)
}
func TestPasswordAndInvalidCredentialsAreSanitized(t *testing.T) {
	service, _ := newService(t)
	_, _, err := service.Register(context.Background(), "a@example.com", "short", domain.RoleReader)
	assert.ErrorIs(t, err, domain.ErrInvalidPassword)
	_, err = service.Login(context.Background(), "none@example.com", "password-1234")
	assert.ErrorIs(t, err, domain.ErrInvalidCredentials)
	assert.False(t, errors.Is(err, domain.ErrUserNotFound))
}
