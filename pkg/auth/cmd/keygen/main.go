// Command keygen prints a new PASETO v4 public signing key pair to stdout.
package main

import "github.com/belLena81/raglibrarian/pkg/auth"

func main() {
	auth.PrintNewKey()
}
