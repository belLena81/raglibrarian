package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yourname/raglibrarian/services/query/internal/application/auth"
	"github.com/yourname/raglibrarian/services/query/internal/domain/user"
)

// ── fakes ─────────────────────────────────────────────────────────────────────

type fakeHasher struct{ failHash bool }

func (f *fakeHasher) Hash(p string) (string, error) {
	if f.failHash {
		return "", errors.New("hasher unavailable")
	}
	return "hashed:" + p, nil
}
func (f *fakeHasher) Compare(hash, plain string) error {
	if hash == "hashed:"+plain {
		return nil
	}
	return errors.New("mismatch")
}

type fakeIssuer struct{ failIssue bool }

func (f *fakeIssuer) Issue(c auth.TokenClaims) (string, error) {
	if f.failIssue {
		return "", errors.New("issuer unavailable")
	}
	return "access:" + c.UserID.String(), nil
}

func newService() (*auth.Service, *user.FakeRepository, *user.FakeRefreshTokenRepository) {
	ur := user.NewFakeRepository()
	tr := user.NewFakeRefreshTokenRepository()
	svc := auth.NewService(ur, tr, &fakeHasher{}, &fakeIssuer{}, auth.DefaultConfig())
	return svc, ur, tr
}

var ctx = context.Background()

// ── Register ──────────────────────────────────────────────────────────────────

func TestRegister_HappyPath(t *testing.T) {
	svc, repo, _ := newService()
	out, err := svc.Register(ctx, auth.RegisterInput{
		Name: "Ada Lovelace", Email: "ada@example.com", Password: "secret99",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, out.UserID)
	assert.Equal(t, user.RoleReader, out.Role)
	assert.NotEmpty(t, out.Tokens.AccessToken)
	assert.NotEmpty(t, out.Tokens.RefreshToken)
	assert.Equal(t, 1, repo.Count())
}

func TestRegister_DuplicateEmail(t *testing.T) {
	svc, _, _ := newService()
	in := auth.RegisterInput{Name: "Ada", Email: "ada@example.com", Password: "secret99"}
	_, err := svc.Register(ctx, in)
	require.NoError(t, err)
	_, err = svc.Register(ctx, in)
	assert.ErrorIs(t, err, user.ErrEmailTaken)
}

func TestRegister_PasswordTooShort(t *testing.T) {
	svc, _, _ := newService()
	_, err := svc.Register(ctx, auth.RegisterInput{Name: "Ada", Email: "ada@example.com", Password: "short"})
	assert.ErrorIs(t, err, auth.ErrPasswordTooShort)
}

func TestRegister_PasswordTooLong(t *testing.T) {
	svc, _, _ := newService()
	_, err := svc.Register(ctx, auth.RegisterInput{
		Name: "Ada", Email: "ada@example.com", Password: string(make([]byte, 73)),
	})
	assert.ErrorIs(t, err, auth.ErrPasswordTooLong)
}

func TestRegister_InvalidEmail(t *testing.T) {
	svc, _, _ := newService()
	_, err := svc.Register(ctx, auth.RegisterInput{Name: "Ada", Email: "bad", Password: "secret99"})
	assert.ErrorIs(t, err, user.ErrInvalidEmail)
}

// ── Login ─────────────────────────────────────────────────────────────────────

func TestLogin_HappyPath(t *testing.T) {
	svc, _, _ := newService()
	_, err := svc.Register(ctx, auth.RegisterInput{Name: "Ada", Email: "ada@example.com", Password: "secret99"})
	require.NoError(t, err)

	out, err := svc.Login(ctx, auth.LoginInput{Email: "ada@example.com", Password: "secret99"})
	require.NoError(t, err)
	assert.NotEmpty(t, out.Tokens.AccessToken)
	assert.NotEmpty(t, out.Tokens.RefreshToken)
}

func TestLogin_WrongPassword(t *testing.T) {
	svc, _, _ := newService()
	_, _ = svc.Register(ctx, auth.RegisterInput{Name: "Ada", Email: "ada@example.com", Password: "secret99"})
	_, err := svc.Login(ctx, auth.LoginInput{Email: "ada@example.com", Password: "wrong"})
	assert.ErrorIs(t, err, user.ErrInvalidPassword)
}

func TestLogin_UnknownEmail(t *testing.T) {
	svc, _, _ := newService()
	_, err := svc.Login(ctx, auth.LoginInput{Email: "ghost@example.com", Password: "secret99"})
	assert.ErrorIs(t, err, user.ErrUserNotFound)
}

func TestLogin_EmailCaseInsensitive(t *testing.T) {
	svc, _, _ := newService()
	_, _ = svc.Register(ctx, auth.RegisterInput{Name: "Ada", Email: "ada@example.com", Password: "secret99"})
	_, err := svc.Login(ctx, auth.LoginInput{Email: "ADA@EXAMPLE.COM", Password: "secret99"})
	require.NoError(t, err)
}

// ── Refresh ───────────────────────────────────────────────────────────────────

func TestRefresh_HappyPath(t *testing.T) {
	svc, _, _ := newService()
	out, _ := svc.Register(ctx, auth.RegisterInput{Name: "Ada", Email: "ada@example.com", Password: "secret99"})

	pair, err := svc.Refresh(ctx, out.Tokens.RefreshToken)
	require.NoError(t, err)
	assert.NotEmpty(t, pair.AccessToken)
	assert.NotEmpty(t, pair.RefreshToken)
	// New refresh token must differ (rotation)
	assert.NotEqual(t, out.Tokens.RefreshToken, pair.RefreshToken)
}

func TestRefresh_TokenRotated_OldTokenInvalid(t *testing.T) {
	svc, _, _ := newService()
	out, _ := svc.Register(ctx, auth.RegisterInput{Name: "Ada", Email: "ada@example.com", Password: "secret99"})
	oldToken := out.Tokens.RefreshToken

	_, err := svc.Refresh(ctx, oldToken)
	require.NoError(t, err)

	// Using the old token again must fail
	_, err = svc.Refresh(ctx, oldToken)
	assert.ErrorIs(t, err, auth.ErrRefreshTokenInvalid)
}

func TestRefresh_InvalidToken(t *testing.T) {
	svc, _, _ := newService()
	_, err := svc.Refresh(ctx, "completelyinvalidtoken")
	assert.ErrorIs(t, err, auth.ErrRefreshTokenInvalid)
}

// ── Logout ────────────────────────────────────────────────────────────────────

func TestLogout_RevokesAllTokens(t *testing.T) {
	svc, _, _ := newService()
	out, _ := svc.Register(ctx, auth.RegisterInput{Name: "Ada", Email: "ada@example.com", Password: "secret99"})

	err := svc.Logout(ctx, out.UserID)
	require.NoError(t, err)

	// Refresh should now fail
	_, err = svc.Refresh(ctx, out.Tokens.RefreshToken)
	assert.ErrorIs(t, err, auth.ErrRefreshTokenInvalid)
}

// ── Me ────────────────────────────────────────────────────────────────────────

func TestMe_ReturnsProfile(t *testing.T) {
	svc, _, _ := newService()
	registered, _ := svc.Register(ctx, auth.RegisterInput{Name: "Ada", Email: "ada@example.com", Password: "secret99"})
	out, err := svc.Me(ctx, registered.UserID)
	require.NoError(t, err)
	assert.Equal(t, "Ada", out.Name)
	assert.Equal(t, "ada@example.com", out.Email)
}

func TestMe_UnknownID(t *testing.T) {
	svc, _, _ := newService()
	_, err := svc.Me(ctx, uuid.New())
	assert.ErrorIs(t, err, user.ErrUserNotFound)
}

// ── SeedAdmin ─────────────────────────────────────────────────────────────────

func TestSeedAdmin_CreatesAdminOnFirstRun(t *testing.T) {
	svc, repo, _ := newService()
	err := svc.SeedAdmin(ctx, "admin@rl.dev", "adminpass1")
	require.NoError(t, err)
	assert.Equal(t, 1, repo.Count())
}

func TestSeedAdmin_Idempotent(t *testing.T) {
	svc, repo, _ := newService()
	require.NoError(t, svc.SeedAdmin(ctx, "admin@rl.dev", "adminpass1"))
	require.NoError(t, svc.SeedAdmin(ctx, "admin@rl.dev", "adminpass1")) // second call is no-op
	assert.Equal(t, 1, repo.Count())
}

func TestSeedAdmin_AdminRoleAssigned(t *testing.T) {
	svc, repo, _ := newService()
	_ = svc.SeedAdmin(ctx, "admin@rl.dev", "adminpass1")
	out, err := svc.Login(ctx, auth.LoginInput{Email: "admin@rl.dev", Password: "adminpass1"})
	require.NoError(t, err)
	_ = repo
	assert.Equal(t, user.RoleAdmin, out.Role)
}
