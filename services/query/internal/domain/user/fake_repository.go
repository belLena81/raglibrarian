package user

import (
	"context"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// ── FakeRepository ────────────────────────────────────────────────────────────

type FakeRepository struct {
	mu    sync.RWMutex
	store map[uuid.UUID]*User
}

func NewFakeRepository() *FakeRepository {
	return &FakeRepository{store: make(map[uuid.UUID]*User)}
}

func (r *FakeRepository) Save(_ context.Context, u *User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.store[u.id]; !exists {
		for _, v := range r.store {
			if strings.EqualFold(v.email, u.email) {
				return ErrEmailTaken
			}
		}
	}
	clone := *u
	r.store[clone.id] = &clone
	return nil
}

func (r *FakeRepository) FindByID(_ context.Context, id uuid.UUID) (*User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	u, ok := r.store[id]
	if !ok {
		return nil, ErrUserNotFound
	}
	clone := *u
	return &clone, nil
}

func (r *FakeRepository) FindByEmail(_ context.Context, email string) (*User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, u := range r.store {
		if strings.EqualFold(u.email, strings.ToLower(strings.TrimSpace(email))) {
			clone := *u
			return &clone, nil
		}
	}
	return nil, ErrUserNotFound
}

func (r *FakeRepository) ExistsByEmail(_ context.Context, email string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, u := range r.store {
		if strings.EqualFold(u.email, strings.ToLower(strings.TrimSpace(email))) {
			return true, nil
		}
	}
	return false, nil
}

func (r *FakeRepository) Seed(u *User) {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *u
	r.store[clone.id] = &clone
}

func (r *FakeRepository) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.store)
}

// ── FakeRefreshTokenRepository ────────────────────────────────────────────────

type FakeRefreshTokenRepository struct {
	mu    sync.RWMutex
	store map[uuid.UUID]*RefreshToken // keyed by token ID
}

func NewFakeRefreshTokenRepository() *FakeRefreshTokenRepository {
	return &FakeRefreshTokenRepository{store: make(map[uuid.UUID]*RefreshToken)}
}

func (r *FakeRefreshTokenRepository) SaveRefreshToken(_ context.Context, t *RefreshToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *t
	r.store[clone.id] = &clone
	return nil
}

func (r *FakeRefreshTokenRepository) FindRefreshTokenByHash(_ context.Context, hash string) (*RefreshToken, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, t := range r.store {
		if t.tokenHash == hash {
			clone := *t
			return &clone, nil
		}
	}
	return nil, ErrTokenNotFound
}

func (r *FakeRefreshTokenRepository) RevokeRefreshToken(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.store[id]
	if !ok {
		return ErrTokenNotFound
	}
	t.Revoke()
	return nil
}

func (r *FakeRefreshTokenRepository) RevokeAllForUser(_ context.Context, userID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.store {
		if t.userID == userID {
			t.Revoke()
		}
	}
	return nil
}
