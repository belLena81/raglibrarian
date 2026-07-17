package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"syscall"

	"golang.org/x/term"
)

const domain = "raglibrarian/admin-bootstrap/v1\x00"

var standardInput = syscall.Stdin

var randomReader io.Reader = rand.Reader

func main() {
	output := flag.String("out", "", "verifier output path")
	flag.Parse()
	if *output == "" || !term.IsTerminal(standardInput) || !term.IsTerminal(int(os.Stderr.Fd())) {
		fail()
	}
	code, err := generateCode()
	if err != nil {
		fail()
	}
	displayCode := string(code)
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
	_, _ = fmt.Fprintf(os.Stderr, "Bootstrap code (store it now; it cannot be recovered): %s\n", displayCode)
	_, _ = fmt.Fprintln(os.Stderr, "Verifier created")
}

func generateCode() ([]byte, error) {
	raw := make([]byte, 20)
	if _, err := io.ReadFull(randomReader, raw); err != nil {
		return nil, err
	}
	return []byte(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)), nil
}

func fail() {
	_, _ = fmt.Fprintln(os.Stderr, errors.New("bootstrap verifier creation failed"))
	os.Exit(1)
}
