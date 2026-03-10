package auth

import (
	"encoding/hex"
	"fmt"

	gopasseto "aidanwoods.dev/go-paseto"
)

// GenerateKeyHex returns a fresh V4 symmetric key as a 64-character hex string.
func GenerateKeyHex() string {
	key := gopasseto.NewV4SymmetricKey()
	return hex.EncodeToString(key.ExportBytes())
}

// PrintNewKey prints a freshly generated AUTH_SECRET_KEY line to stdout.
func PrintNewKey() {
	fmt.Printf("AUTH_SECRET_KEY=%s\n", GenerateKeyHex())
	fmt.Println("# Store this in your secrets manager. Never commit it to git.")
}
