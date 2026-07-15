// Package authflow defines Edge-owned authentication workflow results and failures.
package authflow

import "errors"

// Stable Edge authentication failures used across handlers and adapters.
var (
	ErrInvalidCredentials  = errors.New("invalid credentials")
	ErrInvalidRegistration = errors.New("invalid registration")
	ErrEmailTaken          = errors.New("email already registered")
	ErrUnavailable         = errors.New("identity service unavailable")
)

// Session is returned by an Identity session action. It has no JSON tags so a
// refresh token cannot accidentally become an HTTP response DTO.
type Session struct {
	AccessToken  string
	RefreshToken string
	SessionID    string
	Role         string
}
