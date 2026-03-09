package usecase_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/domain"
	metarepo "github.com/belLena81/raglibrarian/services/metadata/repository"
	"github.com/belLena81/raglibrarian/services/metadata/usecase"
)

// ── Fake repository ───────────────────────────────────────────────────────────

type fakeUserRepo struct {
	users   map[string]domain.User // keyed by email
	saveErr error
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{users: make(map[string]domain.User)}
}

func (f *fakeUserRepo) Save(_ context.Context, u domain.User) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	if _, exists := f.users[u.Email()]; exists {
		return domain.ErrEmailTaken
	}
	f.users[u.Email()] = u
	return nil
}

func (f *fakeUserRepo) FindByEmail(_ context.Context, email string) (domain.User, error) {
	u, ok := f.users[email]
	if !ok {
		return domain.User{}, domain.ErrUserNotFound
	}
	return u, nil
}

func (f *fakeUserRepo) FindByID(_ context.Context, id string) (domain.User, error) {
	for _, u := range f.users {
		if u.ID() == id {
			return u, nil
		}
	}
	return domain.User{}, domain.ErrUserNotFound
}

// Ensure the fake satisfies the interface at compile time.
var _ metarepo.UserRepository = (*fakeUserRepo)(nil)

// ── Helpers ───────────────────────────────────────────────────────────────────

func newTestIssuer(t *testing.T) *auth.Issuer {
	t.Helper()
	issuer, err := auth.NewIssuer(make([]byte, 32), time.Hour)
	require.NoError(t, err)
	return issuer
}

func newService(t *testing.T, repo *fakeUserRepo) *usecase.AuthService {
	t.Helper()
	return usecase.NewAuthService(repo, newTestIssuer(t))
}

// ── Register tests ────────────────────────────────────────────────────────────

func TestRegister_ValidInput_CreatesUser(t *testing.T) {
	svc := newService(t, newFakeUserRepo())

	user, err := svc.Register(context.Background(), "alice@example.com", "password123", domain.RoleReader)

	require.NoError(t, err)
	assert.NotEmpty(t, user.ID())
	assert.Equal(t, "alice@example.com", user.Email())
	assert.Equal(t, domain.RoleReader, user.Role())
	// Password must never be stored as plaintext.
	assert.NotEqual(t, "password123", user.PasswordHash())
}

func TestRegister_AdminRole_Allowed(t *testing.T) {
	svc := newService(t, newFakeUserRepo())

	user, err := svc.Register(context.Background(), "admin@example.com", "pw", domain.RoleAdmin)

	require.NoError(t, err)
	assert.Equal(t, domain.RoleAdmin, user.Role())
}

func TestRegister_DuplicateEmail_ReturnsEmailTakenError(t *testing.T) {
	svc := newService(t, newFakeUserRepo())
	_, err := svc.Register(context.Background(), "dup@example.com", "pw1", domain.RoleReader)
	require.NoError(t, err)

	_, err = svc.Register(context.Background(), "dup@example.com", "pw2", domain.RoleReader)

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrEmailTaken)
}

func TestRegister_InvalidEmail_ReturnsDomainError(t *testing.T) {
	svc := newService(t, newFakeUserRepo())

	_, err := svc.Register(context.Background(), "not-an-email", "pw", domain.RoleReader)

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidEmail)
}

func TestRegister_InvalidRole_ReturnsDomainError(t *testing.T) {
	svc := newService(t, newFakeUserRepo())

	_, err := svc.Register(context.Background(), "a@b.com", "pw", domain.Role("god"))

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidRole)
}

// ── Login tests ───────────────────────────────────────────────────────────────

func TestLogin_ValidCredentials_ReturnsToken(t *testing.T) {
	svc := newService(t, newFakeUserRepo())
	_, err := svc.Register(context.Background(), "bob@example.com", "correct-pw", domain.RoleReader)
	require.NoError(t, err)

	token, err := svc.Login(context.Background(), "bob@example.com", "correct-pw")

	require.NoError(t, err)
	assert.NotEmpty(t, token)
}

func TestLogin_WrongPassword_ReturnsInvalidCredentials(t *testing.T) {
	svc := newService(t, newFakeUserRepo())
	_, _ = svc.Register(context.Background(), "bob@example.com", "correct-pw", domain.RoleReader)

	_, err := svc.Login(context.Background(), "bob@example.com", "wrong-pw")

	require.Error(t, err)
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

func TestLogin_UnknownEmail_ReturnsInvalidCredentials(t *testing.T) {
	// CRITICAL: must return the same error as wrong password.
	// Different errors would allow user enumeration attacks.
	svc := newService(t, newFakeUserRepo())

	_, err := svc.Login(context.Background(), "nobody@example.com", "any-pw")

	require.Error(t, err)
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials,
		"unknown email must return ErrInvalidCredentials, not ErrUserNotFound")
}

func TestLogin_TokenContainsCorrectClaims(t *testing.T) {
	issuer := newTestIssuer(t)
	svc := usecase.NewAuthService(newFakeUserRepo(), issuer)
	_, _ = svc.Register(context.Background(), "carol@example.com", "pw", domain.RoleAdmin)

	token, err := svc.Login(context.Background(), "carol@example.com", "pw")
	require.NoError(t, err)

	claims, err := issuer.Validate(token)
	require.NoError(t, err)
	assert.Equal(t, "carol@example.com", claims.Email)
	assert.Equal(t, domain.RoleAdmin, claims.Role)
}

func TestNewAuthService_NilRepo_Panics(t *testing.T) {
	assert.Panics(t, func() {
		usecase.NewAuthService(nil, newTestIssuer(t))
	})
}

func TestNewAuthService_NilIssuer_Panics(t *testing.T) {
	assert.Panics(t, func() {
		usecase.NewAuthService(newFakeUserRepo(), nil)
	})
}
