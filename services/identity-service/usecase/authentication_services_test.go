package usecase

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/services/identity-service/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/password"
	identitytoken "github.com/belLena81/raglibrarian/services/identity-service/token"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

type fakeUsers struct{ users map[string]domain.User }

func (r *fakeUsers) FindByEmail(_ context.Context, email string) (domain.User, error) {
	user, ok := r.users[email]
	if !ok {
		return domain.User{}, domain.ErrUserNotFound
	}
	return user, nil
}

func (r *fakeUsers) FindByEmailRoles(_ context.Context, email string) ([]domain.User, error) {
	users := make([]domain.User, 0, 3)
	for _, user := range r.users {
		if user.Email() == email {
			users = append(users, user)
		}
	}
	return users, nil
}

type memorySessions struct {
	mu        sync.Mutex
	sessions  map[string]port.Session
	users     map[string]domain.User
	tokens    map[string]string
	consumed  map[string]bool
	revoked   map[string]bool
	next      int
	rotateErr error
}

func newMemorySessions() *memorySessions {
	return &memorySessions{
		sessions: map[string]port.Session{},
		users:    map[string]domain.User{},
		tokens:   map[string]string{},
		consumed: map[string]bool{},
		revoked:  map[string]bool{},
	}
}

func (r *memorySessions) Create(_ context.Context, session port.Session, _ time.Time, hash []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	r.sessions[session.ID] = session
	r.tokens[string(hash)] = session.ID
	return nil
}

func (r *memorySessions) Rotate(
	_ context.Context,
	hash []byte,
	successor []byte,
	_ time.Time,
	prepare port.PrepareRefresh,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rotateErr != nil {
		err := r.rotateErr
		r.rotateErr = nil
		return err
	}
	key := string(hash)
	id, ok := r.tokens[key]
	if !ok || r.revoked[id] {
		return port.ErrRefreshTokenInvalid
	}
	if r.consumed[key] {
		r.revoked[id] = true
		return port.ErrRefreshTokenReused
	}
	session := r.sessions[id]
	user, ok := r.users[session.UserID]
	if !ok {
		return errors.New("load refresh principal")
	}
	if err := prepare(port.RefreshPrincipal{Session: session, UserID: user.ID(), Email: user.Email(), Role: user.Role()}); err != nil {
		return err
	}
	r.consumed[key] = true
	r.tokens[string(successor)] = id
	return nil
}

func (r *memorySessions) Validate(_ context.Context, userID, id string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[id]
	if !ok || session.UserID != userID || r.revoked[id] || !session.ExpiresAt.After(now) {
		return port.ErrSessionInvalid
	}
	return nil
}

func (r *memorySessions) Logout(_ context.Context, id string, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revoked[id] = true
	return nil
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type failingIssuer struct{ err error }

func (i failingIssuer) Issue(string, string, domain.Role, string) (string, error) {
	return "", i.err
}

func newIssuer(t *testing.T) (*auth.Verifier, AccessTokenIssuer) {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	signer, err := auth.NewSigner(privateKey, time.Hour)
	require.NoError(t, err)
	verifier, err := auth.NewVerifier(privateKey.Public().(ed25519.PublicKey))
	require.NoError(t, err)
	return verifier, identitytoken.NewIssuer(signer)
}

func TestLoginSigningFailureDoesNotPersistSession(t *testing.T) {
	passwords := password.NewLimitedHasher(password.BcryptHasher{}, 1)
	hash, err := passwords.Hash(context.Background(), "password-1234")
	require.NoError(t, err)
	user := activeTestUser(t, "reader@example.com", hash)
	sessions := newMemorySessions()
	issuerErr := errors.New("signing failed")
	service := NewSessionService(
		&fakeUsers{users: map[string]domain.User{user.Email(): user}},
		sessions,
		failingIssuer{err: issuerErr},
		passwords,
		fixedClock{now: time.Now().UTC()},
		time.Hour,
	)

	_, err = service.Login(context.Background(), user.Email(), "password-1234")

	assert.ErrorIs(t, err, issuerErr)
	assert.Empty(t, sessions.sessions)
}

func TestLoginAcceptsStoredBcryptPassword(t *testing.T) {
	storedHash, err := bcrypt.GenerateFromPassword([]byte("password-1234"), bcrypt.MinCost)
	require.NoError(t, err)
	user := activeTestUser(t, "reader@example.com", string(storedHash))
	_, tokenIssuer := newIssuer(t)
	sessions := newMemorySessions()
	service := NewSessionService(
		&fakeUsers{users: map[string]domain.User{user.Email(): user}},
		sessions,
		tokenIssuer,
		password.NewLimitedHasher(password.BcryptHasher{}, 1),
		fixedClock{now: time.Now().UTC()},
		time.Hour,
	)

	result, err := service.Login(context.Background(), "  READER@EXAMPLE.COM ", "password-1234")

	require.NoError(t, err)
	assert.NotEmpty(t, result.AccessToken)
	assert.Contains(t, sessions.sessions, result.SessionID)
}

func TestRefreshPreparationFailureLeavesOriginalTokenRetryable(t *testing.T) {
	issuer, tokenIssuer := newIssuer(t)
	now := time.Now().UTC()
	user := activeTestUser(t, "reader@example.com", "hash")
	sessions := newMemorySessions()
	sessions.users[user.ID()] = user
	currentToken := "current-refresh-token"
	created := newSession(user.ID(), now.Add(time.Hour))
	require.NoError(t, sessions.Create(context.Background(), created, now, hashRefreshToken(currentToken)))
	users := &fakeUsers{users: map[string]domain.User{user.Email(): user}}
	service := NewSessionService(users, sessions, failingIssuer{err: errors.New("signing failed")}, password.NewLimitedHasher(password.BcryptHasher{}, 1), fixedClock{now: now}, time.Hour)

	_, err := service.Refresh(context.Background(), currentToken)
	require.Error(t, err)
	service.issuer = tokenIssuer
	result, err := service.Refresh(context.Background(), currentToken)
	require.NoError(t, err)
	claims, err := issuer.Validate(result.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, created.ID, claims.SessionID)
}

func TestRefreshPrincipalFailureLeavesOriginalTokenRetryable(t *testing.T) {
	_, tokenIssuer := newIssuer(t)
	now := time.Now().UTC()
	user := activeTestUser(t, "reader@example.com", "hash")
	sessions := newMemorySessions()
	sessions.users[user.ID()] = user
	currentToken := "current-refresh-token"
	created := newSession(user.ID(), now.Add(time.Hour))
	require.NoError(t, sessions.Create(context.Background(), created, now, hashRefreshToken(currentToken)))
	sessions.rotateErr = errors.New("database unavailable")
	service := NewSessionService(&fakeUsers{users: map[string]domain.User{}}, sessions, tokenIssuer, password.NewLimitedHasher(password.BcryptHasher{}, 1), fixedClock{now: now}, time.Hour)

	_, err := service.Refresh(context.Background(), currentToken)
	require.Error(t, err)
	_, err = service.Refresh(context.Background(), currentToken)
	require.NoError(t, err)
}

func TestRefreshRotatesAndReuseRevokesSession(t *testing.T) {
	_, tokenIssuer := newIssuer(t)
	now := time.Now().UTC()
	user := activeTestUser(t, "reader@example.com", "hash")
	sessions := newMemorySessions()
	sessions.users[user.ID()] = user
	currentToken := "current-refresh-token"
	created := newSession(user.ID(), now.Add(time.Hour))
	require.NoError(t, sessions.Create(context.Background(), created, now, hashRefreshToken(currentToken)))
	service := NewSessionService(&fakeUsers{users: map[string]domain.User{}}, sessions, tokenIssuer, password.NewLimitedHasher(password.BcryptHasher{}, 1), fixedClock{now: now}, time.Hour)

	next, err := service.Refresh(context.Background(), currentToken)
	require.NoError(t, err)
	_, err = service.Refresh(context.Background(), currentToken)
	assert.ErrorIs(t, err, domain.ErrInvalidCredentials)
	assert.ErrorIs(t, service.ValidateSession(context.Background(), user.ID(), created.ID), domain.ErrInvalidCredentials)
	_, err = service.Refresh(context.Background(), next.RefreshToken)
	assert.ErrorIs(t, err, domain.ErrInvalidCredentials)
}

func TestInvalidCredentialsAreSanitized(t *testing.T) {
	_, tokenIssuer := newIssuer(t)
	sessions := NewSessionService(&fakeUsers{users: map[string]domain.User{}}, newMemorySessions(), tokenIssuer, password.NewLimitedHasher(password.BcryptHasher{}, 1), fixedClock{now: time.Now().UTC()}, time.Hour)
	_, err := sessions.Login(context.Background(), "none@example.com", "password-1234")
	assert.ErrorIs(t, err, domain.ErrInvalidCredentials)
	assert.False(t, errors.Is(err, domain.ErrUserNotFound))
}

func TestShortPasswordMismatchDoesNotRevealAccountExistence(t *testing.T) {
	storedHash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	require.NoError(t, err)
	user := activeTestUser(t, "reader@example.com", string(storedHash))
	_, tokenIssuer := newIssuer(t)
	service := NewSessionService(
		&fakeUsers{users: map[string]domain.User{user.Email(): user}},
		newMemorySessions(),
		tokenIssuer,
		password.NewLimitedHasher(password.BcryptHasher{}, 1),
		fixedClock{now: time.Now().UTC()},
		time.Hour,
	)

	_, knownErr := service.Login(context.Background(), user.Email(), "wrong")
	_, unknownErr := service.Login(context.Background(), "unknown@example.com", "wrong")

	assert.ErrorIs(t, knownErr, domain.ErrInvalidCredentials)
	assert.ErrorIs(t, unknownErr, domain.ErrInvalidCredentials)
	assert.Equal(t, knownErr, unknownErr)
}

func TestDummyPasswordHashIsAValidBcryptMismatch(t *testing.T) {
	err := (password.BcryptHasher{}).Compare(context.Background(), dummyPasswordHash, "password-1234")
	assert.ErrorIs(t, err, domain.ErrInvalidCredentials)
}

func activeTestUser(t *testing.T, email, passwordHash string) domain.User {
	t.Helper()
	now := time.Now().UTC()
	user, err := domain.NewVerifiedUser(
		uuid.NewString(), "Reader", email, make([]byte, 32), passwordHash,
		domain.RoleReader, domain.StatusActive, now, now,
	)
	require.NoError(t, err)
	return user
}
