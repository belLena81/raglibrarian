// Package authflow defines Edge-owned authentication workflow results and failures.
package authflow

import "errors"

// Stable Edge authentication failures used across handlers and adapters.
var (
	ErrInvalidCredentials  = errors.New("invalid credentials")
	ErrInvalidRegistration = errors.New("invalid registration")
	ErrUnavailable         = errors.New("identity service unavailable")
	ErrForbidden           = errors.New("operation forbidden")
	ErrConflict            = errors.New("identity state conflict")
	ErrInvalidVerification = errors.New("verification is invalid")
)

// Session is returned by an Identity session action. It has no JSON tags so a
// refresh token cannot accidentally become an HTTP response DTO.
type Session struct {
	AccessToken  string
	RefreshToken string
	SessionID    string
	Role         string
}

// Principal contains current Identity-owned authorization facts. Edge may
// expose this profile but must never derive it from access-token claims.
type Principal struct {
	UserID    string
	SessionID string
	Name      string
	Email     string
	Role      string
	Status    string
}

// IsActiveAdmin reports whether the current Identity facts authorize admin work.
func (p Principal) IsActiveAdmin() bool { return p.Status == "active" && p.Role == "admin" }

// PendingLibrarian is the public profile of a librarian awaiting review.
type PendingLibrarian struct {
	UserID       string
	Name         string
	Email        string
	RegisteredAt string
}

// PendingPage contains one bounded page of librarians awaiting review.
type PendingPage struct {
	Users         []PendingLibrarian
	NextPageToken string
}
