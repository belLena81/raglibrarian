package securevalue_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/services/identity-service/securevalue"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

func TestProtectorEncryptsVerificationPayloadAndNormalizesFingerprint(t *testing.T) {
	protector, err := securevalue.New(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), "key-v1")
	require.NoError(t, err)

	sealed, err := protector.SealVerification("message-1", "Reader@Example.TEST", "verification-token")
	require.NoError(t, err)
	assert.NotContains(t, string(sealed.Ciphertext), "Reader@Example.TEST")
	assert.NotContains(t, string(sealed.Ciphertext), "verification-token")

	email, token, err := protector.OpenVerification(port.EmailDelivery{
		ID: sealed.ID, KeyID: sealed.KeyID, Nonce: sealed.Nonce, Ciphertext: sealed.Ciphertext,
	})
	require.NoError(t, err)
	assert.Equal(t, "Reader@Example.TEST", email)
	assert.Equal(t, "verification-token", token)
	assert.Equal(t, protector.Fingerprint(" reader@example.test "), protector.Fingerprint("READER@EXAMPLE.TEST"))
}

func TestProtectorBindsCiphertextToMessageAndKeyID(t *testing.T) {
	protector, err := securevalue.New(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), "key-v1")
	require.NoError(t, err)
	sealed, err := protector.SealVerification("message-1", "reader@example.test", "verification-token")
	require.NoError(t, err)

	_, _, err = protector.OpenVerification(port.EmailDelivery{ID: "message-2", KeyID: sealed.KeyID, Nonce: sealed.Nonce, Ciphertext: sealed.Ciphertext})
	assert.ErrorIs(t, err, securevalue.ErrInvalidPayload)
}
