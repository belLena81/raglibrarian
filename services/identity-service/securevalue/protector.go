// Package securevalue implements Identity-owned cryptographic adapters for
// pseudonymous lookup and encrypted email-outbox payloads.
package securevalue

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

// ErrInvalidPayload reports invalid key material, ciphertext, or verification
// payloads without exposing sensitive values.
var ErrInvalidPayload = errors.New("secure value payload is invalid")

const (
	fingerprintDomain = "raglibrarian/email-fingerprint/v1\x00"
	outboxDomain      = "raglibrarian/email-outbox/v1\x00"
)

// Protector creates deterministic email fingerprints and authenticated,
// encrypted verification-email payloads.
type Protector struct {
	fingerprintKey []byte
	aead           cipher.AEAD
	keyID          string
}

// New constructs a Protector from distinct 256-bit fingerprint and encryption
// keys and a non-empty encryption key identifier.
func New(fingerprintKey, encryptionKey []byte, keyID string) (*Protector, error) {
	if len(fingerprintKey) != 32 || len(encryptionKey) != 32 || strings.TrimSpace(keyID) == "" {
		return nil, ErrInvalidPayload
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, ErrInvalidPayload
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrInvalidPayload
	}
	return &Protector{
		fingerprintKey: append([]byte(nil), fingerprintKey...),
		aead:           aead,
		keyID:          keyID,
	}, nil
}

// Fingerprint returns a domain-separated HMAC of a normalized email address.
func (p *Protector) Fingerprint(email string) []byte {
	mac := hmac.New(sha256.New, p.fingerprintKey)
	_, _ = mac.Write([]byte(fingerprintDomain))
	_, _ = mac.Write([]byte(strings.ToLower(strings.TrimSpace(email))))
	return mac.Sum(nil)
}

type verificationPayload struct {
	Email string `json:"email"`
	Token string `json:"token"`
}

// SealVerification encrypts and authenticates an email and verification token
// for storage in the Identity-owned outbox.
func (p *Protector) SealVerification(messageID, email, token string) (port.SealedEmail, error) {
	if strings.TrimSpace(messageID) == "" || strings.TrimSpace(email) == "" || strings.TrimSpace(token) == "" {
		return port.SealedEmail{}, ErrInvalidPayload
	}
	plaintext, err := json.Marshal(verificationPayload{Email: email, Token: token})
	if err != nil {
		return port.SealedEmail{}, ErrInvalidPayload
	}
	nonce := make([]byte, p.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return port.SealedEmail{}, ErrInvalidPayload
	}
	aad := []byte(outboxDomain + messageID + "\x00" + p.keyID)
	ciphertext := p.aead.Seal(nil, nonce, plaintext, aad)
	return port.SealedEmail{
		ID: messageID, MessageType: "verify_registration", KeyID: p.keyID,
		Nonce: nonce, Ciphertext: ciphertext, CreatedAt: time.Now().UTC(),
	}, nil
}

func (p *Protector) SealPasswordReset(messageID, email, code string) (port.SealedEmail, error) {
	sealed, err := p.SealVerification(messageID, email, code)
	if err != nil {
		return port.SealedEmail{}, err
	}
	sealed.MessageType = "password_reset_code"
	return sealed, nil
}

// OpenVerification authenticates and decrypts one leased verification message.
func (p *Protector) OpenVerification(delivery port.EmailDelivery) (string, string, error) {
	if delivery.KeyID != p.keyID || delivery.ID == "" {
		return "", "", ErrInvalidPayload
	}
	aad := []byte(outboxDomain + delivery.ID + "\x00" + delivery.KeyID)
	plaintext, err := p.aead.Open(nil, delivery.Nonce, delivery.Ciphertext, aad)
	if err != nil {
		return "", "", ErrInvalidPayload
	}
	var payload verificationPayload
	if err = json.Unmarshal(plaintext, &payload); err != nil || payload.Email == "" || payload.Token == "" {
		return "", "", ErrInvalidPayload
	}
	return payload.Email, payload.Token, nil
}
