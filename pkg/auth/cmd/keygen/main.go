// Command keygen prints a new PASETO v4 symmetric key to stdout.
package main

import "github.com/belLena81/raglibrarian/pkg/auth"

func main() {
	auth.PrintNewKey()
}
