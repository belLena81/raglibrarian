package auth

import (
	"encoding/hex"
	"fmt"

	gopasseto "aidanwoods.dev/go-paseto"
)

// GenerateKeyHex generates a fresh V4 symmetric key and returns it as a
// 64-character hex string ready to paste into AUTH_SECRET_KEY.
//
// This follows the article's SecureKeyLoader.GenerateAndPrintKey pattern:
//
//	key := paseto.NewV4SymmetricKey()
//	keyBytes := key.ExportBytes()
//	fmt.Printf("Generated key: %s\n", hex.EncodeToString(keyBytes))
//
// Usage: go run ./cmd/keygen   (see Makefile keygen target)
func GenerateKeyHex() string {
	key := gopasseto.NewV4SymmetricKey()
	return hex.EncodeToString(key.ExportBytes())
}

// PrintNewKey prints a freshly generated key to stdout.
// Intended for one-off use by operators setting up a new deployment.
func PrintNewKey() {
	fmt.Printf("AUTH_SECRET_KEY=%s\n", GenerateKeyHex())
	fmt.Println("# Store this in your secrets manager. Never commit it to git.")
}
