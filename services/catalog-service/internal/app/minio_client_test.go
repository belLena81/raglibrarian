package app

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/catalog-service/config"
)

func TestMinIOTransportIsSecureByDefault(t *testing.T) {
	client, transport, err := newMinIOClient(config.Config{MinIOEndpoint: "storage.internal:9000", MinIOAccessKey: "access", MinIOSecretKey: "secret"})
	if err != nil {
		t.Fatalf("newMinIOClient(): %v", err)
	}
	defer transport.CloseIdleConnections()
	if client.EndpointURL().Scheme != "https" {
		t.Fatalf("scheme = %q", client.EndpointURL().Scheme)
	}
}

func TestMinIOTransportAllowsExplicitInsecureMode(t *testing.T) {
	client, transport, err := newMinIOClient(config.Config{MinIOEndpoint: "minio:9000", MinIOAccessKey: "access", MinIOSecretKey: "secret", MinIOInsecure: true})
	if err != nil {
		t.Fatalf("newMinIOClient(): %v", err)
	}
	defer transport.CloseIdleConnections()
	if client.EndpointURL().Scheme != "http" {
		t.Fatalf("scheme = %q", client.EndpointURL().Scheme)
	}
}

func TestLoadPrivateCAPoolAllowsReadOnlySymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "mounted-ca.pem")
	if err := os.WriteFile(target, testCertificatePEM(t), 0o444); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "ca.pem")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPrivateCAPool(link); err != nil {
		t.Fatalf("loadPrivateCAPool() symlink: %v", err)
	}
}

func TestLoadPrivateCAPool(t *testing.T) {
	directory := t.TempDir()
	validPath := filepath.Join(directory, "ca.pem")
	if err := os.WriteFile(validPath, testCertificatePEM(t), 0o444); err != nil {
		t.Fatal(err)
	}
	pool, err := loadPrivateCAPool(validPath)
	if err != nil {
		t.Fatalf("loadPrivateCAPool(): %v", err)
	}
	if len(pool.Subjects()) != 1 {
		t.Fatalf("subjects = %d", len(pool.Subjects()))
	}

	invalidPath := filepath.Join(directory, "invalid.pem")
	if err = os.WriteFile(invalidPath, []byte("not a certificate"), 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err = loadPrivateCAPool(invalidPath); err == nil {
		t.Fatal("expected malformed CA rejection")
	}

	writablePath := filepath.Join(directory, "writable.pem")
	if err = os.WriteFile(writablePath, testCertificatePEM(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = loadPrivateCAPool(writablePath); err == nil {
		t.Fatal("expected writable CA rejection")
	}
}

func testCertificatePEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Catalog test CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
