package app

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/belLena81/raglibrarian/services/catalog-service/config"
)

const maximumMinIOCABytes = 1 << 20

func newMinIOClient(cfg config.Config) (*minio.Client, *http.Transport, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.MinIOInsecure {
		// Plaintext local development must not inherit a proxy that could receive
		// object-store credentials outside the isolated local network.
		transport.Proxy = nil
	}
	if cfg.MinIOCAFile != "" {
		if cfg.MinIOInsecure {
			transport.CloseIdleConnections()
			return nil, nil, errors.New("private CA cannot be used with insecure object storage")
		}
		pool, err := loadPrivateCAPool(cfg.MinIOCAFile)
		if err != nil {
			transport.CloseIdleConnections()
			return nil, nil, err
		}
		transport.TLSClientConfig.RootCAs = pool
	}
	client, err := minio.New(cfg.MinIOEndpoint, &minio.Options{
		Creds:           credentials.NewStaticV4(cfg.MinIOAccessKey, cfg.MinIOSecretKey, ""),
		Secure:          !cfg.MinIOInsecure,
		Transport:       transport,
		TrailingHeaders: true,
	})
	if err != nil {
		transport.CloseIdleConnections()
		return nil, nil, err
	}
	return client, transport, nil
}

func loadPrivateCAPool(path string) (*x509.CertPool, error) {
	file, err := os.Open(path) // #nosec G304 -- operator-provided CA path is validated through its opened descriptor.
	if err != nil {
		return nil, errors.New("CATALOG_MINIO_CA_FILE is invalid")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o222 != 0 || info.Size() < 1 || info.Size() > maximumMinIOCABytes {
		return nil, errors.New("CATALOG_MINIO_CA_FILE is invalid")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximumMinIOCABytes+1))
	if err != nil || len(contents) > maximumMinIOCABytes {
		return nil, errors.New("CATALOG_MINIO_CA_FILE is invalid")
	}
	pool := x509.NewCertPool()
	remaining := contents
	certificates := 0
	for len(remaining) > 0 {
		block, rest := pemDecodeCertificate(remaining)
		if block == nil {
			return nil, errors.New("CATALOG_MINIO_CA_FILE is invalid")
		}
		certificate, parseErr := x509.ParseCertificate(block)
		if parseErr != nil || !certificate.IsCA {
			return nil, errors.New("CATALOG_MINIO_CA_FILE is invalid")
		}
		pool.AddCert(certificate)
		certificates++
		remaining = rest
	}
	if certificates == 0 {
		return nil, errors.New("CATALOG_MINIO_CA_FILE is invalid")
	}
	return pool, nil
}

func pemDecodeCertificate(contents []byte) ([]byte, []byte) {
	block, rest := pem.Decode(contents)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
		return nil, nil
	}
	return block.Bytes, rest
}
