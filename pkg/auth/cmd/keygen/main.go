// Command keygen prints a new PASETO v4 symmetric key to stdout.
// Run once when setting up a new deployment:
//
//	go run ./cmd/keygen
//
// Then store the output in your secrets manager as AUTH_SECRET_KEY.
package main

import "github.com/belLena81/raglibrarian/pkg/auth"

func main() {
	auth.PrintNewKey()
}
