// Package token adapts Identity subjects to the shared PASETO implementation.
package token

import (
	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/services/identity-service/domain"
)

// Issuer maps Identity-owned user values to the shared token contract.
type Issuer struct{ signer *auth.Signer }

// NewIssuer constructs the Identity token adapter.
func NewIssuer(signer *auth.Signer) *Issuer {
	if signer == nil {
		panic("token: signer must not be nil")
	}
	return &Issuer{signer: signer}
}

// Issue creates an access token without exposing the User aggregate.
func (i *Issuer) Issue(userID, email string, role domain.Role, sessionID string) (string, error) {
	return i.signer.Issue(auth.Subject{UserID: userID, SessionID: sessionID})
}
