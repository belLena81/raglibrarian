//go:build miniointegration

package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestMinIOObjectStoreCleansFailedMultipartUploads(t *testing.T) {
	if os.Getenv("CATALOG_MINIO_INTEGRATION") != "true" {
		t.Skip("CATALOG_MINIO_INTEGRATION is required")
	}

	client := integrationMinIOClient(t)
	bucket := os.Getenv("CATALOG_MINIO_BUCKET")
	store := NewMinIOObjectStore(client, bucket)

	t.Run("reader failure", func(t *testing.T) {
		key := integrationObjectKey(t)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		_, err := store.Put(ctx, key, &failingReader{remaining: 6 << 20})

		if err == nil {
			t.Fatal("failed reader upload unexpectedly succeeded")
		}
		assertNoPartialObject(t, client, bucket, key)
	})

	t.Run("cancellation", func(t *testing.T) {
		key := integrationObjectKey(t)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		reader := &cancellingReader{remaining: 6 << 20, cancel: cancel}

		_, err := store.Put(ctx, key, reader)

		if err == nil {
			t.Fatal("cancelled upload unexpectedly succeeded")
		}
		assertNoPartialObject(t, client, bucket, key)
	})

	t.Run("out of prefix is denied", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.PutObject(ctx, bucket, "outside/"+randomSuffix(t), strings.NewReader("denied"), -1, minio.PutObjectOptions{})
		if err == nil || minio.ToErrorResponse(err).Code != "AccessDenied" {
			t.Fatal("out-of-prefix object write was not denied")
		}
	})
}

func integrationMinIOClient(t *testing.T) *minio.Client {
	t.Helper()
	endpoint := os.Getenv("CATALOG_MINIO_ENDPOINT")
	bucket := os.Getenv("CATALOG_MINIO_BUCKET")
	accessKey := readIntegrationSecret(t, "CATALOG_MINIO_ACCESS_KEY_FILE")
	secretKey := readIntegrationSecret(t, "CATALOG_MINIO_SECRET_KEY_FILE")
	if endpoint == "" || bucket == "" {
		t.Fatal("MinIO integration configuration is incomplete")
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: false,
	})
	if err != nil {
		t.Fatal("MinIO integration client could not be created")
	}
	return client
}

func readIntegrationSecret(t *testing.T, variable string) string {
	t.Helper()
	path := os.Getenv(variable)
	value, err := os.ReadFile(path) // #nosec G703 -- test-only Compose secret path.
	if err != nil || len(value) == 0 {
		t.Fatal("MinIO integration secret is unavailable")
	}
	return strings.TrimSpace(string(value))
}

func integrationObjectKey(t *testing.T) string {
	t.Helper()
	return "originals/integration-" + randomSuffix(t) + ".pdf"
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatal("random test identifier could not be generated")
	}
	return hex.EncodeToString(value[:])
}

func assertNoPartialObject(t *testing.T, client *minio.Client, bucket, key string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err == nil || minio.ToErrorResponse(err).Code != "NoSuchKey" {
		t.Fatal("failed upload left a completed object")
	}
	for upload := range client.ListIncompleteUploads(ctx, bucket, key, true) {
		if upload.Err != nil {
			t.Fatal("incomplete-upload verification failed")
		}
		if upload.Key == key {
			t.Fatal("failed upload left an incomplete object")
		}
	}
}

type failingReader struct {
	remaining int
}

func (r *failingReader) Read(target []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := min(len(target), r.remaining)
	for i := range target[:n] {
		target[i] = 'x'
	}
	r.remaining -= n
	return n, nil
}

type cancellingReader struct {
	remaining int
	cancel    context.CancelFunc
	cancelled bool
}

func (r *cancellingReader) Read(target []byte) (int, error) {
	if r.remaining == 0 {
		if !r.cancelled {
			r.cancelled = true
			r.cancel()
		}
		return 0, context.Canceled
	}
	n := min(len(target), r.remaining)
	for i := range target[:n] {
		target[i] = 'x'
	}
	r.remaining -= n
	return n, nil
}

var _ io.Reader = (*failingReader)(nil)
var _ io.Reader = (*cancellingReader)(nil)
