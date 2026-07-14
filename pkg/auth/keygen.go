package auth

import (
	"encoding/hex"
	"fmt"

	gopasseto "aidanwoods.dev/go-paseto"
)

// GenerateKeyPairHex returns a fresh PASETO v4 public signing key pair.
func GenerateKeyPairHex() (signingKey, verificationKey string) {
	key := gopasseto.NewV4AsymmetricSecretKey()
	return hex.EncodeToString(key.ExportBytes()), hex.EncodeToString(key.Public().ExportBytes())
}

// PrintNewKey prints fresh private/public key configuration without reusing a
// signing secret outside identity-service.
func PrintNewKey() {
	signingKey, verificationKey := GenerateKeyPairHex()
	fmt.Printf("IDENTITY_SIGNING_KEY=%s\n", signingKey)
	fmt.Printf("EDGE_VERIFY_KEY=%s\n", verificationKey)
	fmt.Println("# Store the signing key only in Identity's secret manager.")
}
