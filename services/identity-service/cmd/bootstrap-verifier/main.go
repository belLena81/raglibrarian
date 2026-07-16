package main

import (
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/term"
)

const domain = "raglibrarian/admin-bootstrap/v1\x00"

var standardInput = syscall.Stdin

func main() {
	output := flag.String("out", "", "verifier output path")
	flag.Parse()
	if *output == "" || !term.IsTerminal(standardInput) {
		fail()
	}
	_, _ = fmt.Fprint(os.Stderr, "Bootstrap code: ")
	code, err := term.ReadPassword(standardInput)
	_, _ = fmt.Fprintln(os.Stderr)
	if err != nil || len(code) != 32 {
		fail()
	}
	sum := sha256.Sum256(append([]byte(domain), code...))
	for index := range code {
		code[index] = 0
	}
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- explicit operator-selected output.
	if err != nil {
		fail()
	}
	if _, err = file.Write(sum[:]); err != nil {
		_ = file.Close()
		_ = os.Remove(*output)
		fail()
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(*output)
		fail()
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(*output)
		fail()
	}
	if err = os.Chmod(*output, 0o400); err != nil {
		_ = os.Remove(*output)
		fail()
	}
	_, _ = fmt.Fprintln(os.Stderr, "Verifier created")
}

func fail() {
	_, _ = fmt.Fprintln(os.Stderr, errors.New("bootstrap verifier creation failed"))
	os.Exit(1)
}
