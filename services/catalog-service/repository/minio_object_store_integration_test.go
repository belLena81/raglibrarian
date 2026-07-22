//go:build miniointegration

package repository

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestMinIOObjectStoreVerifiesSuccessfulMultipartUpload(t *testing.T) {
	if os.Getenv("CATALOG_MINIO_INTEGRATION") != "true" {
		t.Skip("CATALOG_MINIO_INTEGRATION is required")
	}

	client := integrationMinIOClient(t)
	bucket := os.Getenv("CATALOG_MINIO_BUCKET")
	store := NewMinIOObjectStore(client, bucket)
	key := integrationObjectKey(t)
	payload := bytes.Repeat([]byte("r2-compatible-integrity-check\n"), 220_000)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_ = client.RemoveObject(cleanupCtx, bucket, key, minio.RemoveObjectOptions{})
	})

	receipt, err := store.Put(ctx, key, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("verified multipart upload failed: %v", err)
	}
	if receipt.Size != int64(len(payload)) || receipt.ChecksumCRC32C == "" {
		t.Fatalf("invalid object receipt: %#v", receipt)
	}
	object, err := client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := io.ReadAll(io.LimitReader(object, int64(len(payload))+1))
	_ = object.Close()
	if err != nil || len(stored) != len(payload) {
		t.Fatal("uploaded object could not be read completely")
	}
	if sha256.Sum256(stored) != sha256.Sum256(payload) {
		t.Fatal("uploaded object digest mismatch")
	}
	if err = store.Delete(ctx, key); err != nil {
		t.Fatalf("delete uploaded object: %v", err)
	}
	if _, err = client.StatObject(ctx, bucket, key, minio.StatObjectOptions{}); err == nil || minio.ToErrorResponse(err).Code != "NoSuchKey" {
		t.Fatal("deleted object remains readable")
	}
}

func TestS3CredentialBoundaries(t *testing.T) {
	if os.Getenv("CATALOG_OBJECT_STORE_BOUNDARY_TEST") != "true" {
		t.Skip("CATALOG_OBJECT_STORE_BOUNDARY_TEST is required")
	}

	originalBucket := os.Getenv("CATALOG_MINIO_BUCKET")
	artifactBucket := os.Getenv("CATALOG_ARTIFACT_BUCKET")
	if originalBucket == "" || artifactBucket == "" || originalBucket == artifactBucket {
		t.Fatal("object-store boundary buckets are invalid")
	}
	catalogClient := integrationMinIOClient(t)
	ingestionClient := integrationClient(t, "INGESTION_MINIO_ACCESS_KEY_FILE", "INGESTION_MINIO_SECRET_KEY_FILE")
	retrievalClient := integrationClient(t, "RETRIEVAL_MINIO_ACCESS_KEY_FILE", "RETRIEVAL_MINIO_SECRET_KEY_FILE")
	originalStore := NewMinIOObjectStore(catalogClient, originalBucket)
	originalKey := integrationObjectKey(t)
	forbiddenOriginalKey := originalKey + ".forbidden"
	artifactKey := "books/integration-" + randomSuffix(t) + "/manifest.json"
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_ = originalStore.Delete(cleanupCtx, originalKey)
		_ = originalStore.Delete(cleanupCtx, forbiddenOriginalKey)
		_ = ingestionClient.RemoveObject(cleanupCtx, artifactBucket, artifactKey, minio.RemoveObjectOptions{})
	})

	if _, err := originalStore.Put(ctx, originalKey, strings.NewReader("authorized synthetic PDF")); err != nil {
		t.Fatalf("catalog original upload: %v", err)
	}
	ingestionSource, err := ingestionClient.GetObject(ctx, originalBucket, originalKey, minio.GetObjectOptions{})
	if err != nil {
		t.Fatalf("ingestion source read: %v", err)
	}
	if _, err = ingestionSource.Stat(); err != nil {
		_ = ingestionSource.Close()
		t.Fatalf("ingestion source stat: %v", err)
	}
	_ = ingestionSource.Close()
	if _, err = ingestionClient.PutObject(ctx, originalBucket, forbiddenOriginalKey, strings.NewReader("denied"), 6, minio.PutObjectOptions{}); err == nil || minio.ToErrorResponse(err).StatusCode != 403 {
		t.Fatal("ingestion credential unexpectedly wrote an original")
	}
	if err = ingestionClient.RemoveObject(ctx, originalBucket, originalKey, minio.RemoveObjectOptions{}); err == nil || minio.ToErrorResponse(err).StatusCode != 403 {
		t.Fatal("ingestion credential unexpectedly deleted an original")
	}
	assertAccessDenied(t, retrievalClient, originalBucket, originalKey)

	artifact := []byte(`{"schema_version":"v1","synthetic":true}`)
	if _, err = ingestionClient.PutObject(ctx, artifactBucket, artifactKey, bytes.NewReader(artifact), int64(len(artifact)), minio.PutObjectOptions{ContentType: "application/json"}); err != nil {
		t.Fatalf("ingestion artifact write: %v", err)
	}
	retrieved, err := retrievalClient.GetObject(ctx, artifactBucket, artifactKey, minio.GetObjectOptions{})
	if err != nil {
		t.Fatalf("retrieval artifact read: %v", err)
	}
	if _, err = retrieved.Stat(); err != nil {
		_ = retrieved.Close()
		t.Fatalf("retrieval artifact stat: %v", err)
	}
	_ = retrieved.Close()
	assertAccessDenied(t, catalogClient, artifactBucket, artifactKey)
	if _, err = retrievalClient.PutObject(ctx, artifactBucket, artifactKey+".forbidden", strings.NewReader("denied"), 6, minio.PutObjectOptions{}); err == nil || minio.ToErrorResponse(err).StatusCode != 403 {
		t.Fatal("retrieval credential unexpectedly wrote an artifact")
	}
	if err = ingestionClient.RemoveObject(ctx, artifactBucket, artifactKey, minio.RemoveObjectOptions{}); err != nil {
		t.Fatalf("ingestion artifact delete: %v", err)
	}
	if err = originalStore.Delete(ctx, originalKey); err != nil {
		t.Fatalf("catalog original delete: %v", err)
	}
}

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

	if integrationBool(t, "CATALOG_MINIO_EXPECT_PREFIX_DENY", true) {
		t.Run("out of prefix is denied", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := client.PutObject(ctx, bucket, "outside/"+randomSuffix(t), strings.NewReader("denied"), -1, minio.PutObjectOptions{})
			if err == nil || minio.ToErrorResponse(err).Code != "AccessDenied" {
				t.Fatal("out-of-prefix object write was not denied")
			}
		})
	}
}

func TestMinIOObjectStoreListsBoundedContinuation(t *testing.T) {
	if os.Getenv("CATALOG_MINIO_INTEGRATION") != "true" {
		t.Skip("CATALOG_MINIO_INTEGRATION is required")
	}

	client := integrationMinIOClient(t)
	bucket := os.Getenv("CATALOG_MINIO_BUCKET")
	store := NewMinIOObjectStore(client, bucket)
	prefix := "originals/zz-listing-" + randomSuffix(t) + "/"
	keys := []string{prefix + "a.pdf", prefix + "b.pdf", prefix + "c.pdf"}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, key := range keys {
		if _, err := client.PutObject(ctx, bucket, key, strings.NewReader("x"), 1, minio.PutObjectOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		for _, key := range keys {
			_ = client.RemoveObject(cleanupCtx, bucket, key, minio.RemoveObjectOptions{})
		}
	})

	first, cursor, err := store.ListCompleted(ctx, "originals/", prefix, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].Reference != keys[0] || first[1].Reference != keys[1] || cursor != keys[1] {
		t.Fatalf("first page = %#v, cursor = %q", first, cursor)
	}
	second, _, err := store.ListCompleted(ctx, "originals/", cursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) == 0 || second[0].Reference != keys[2] {
		t.Fatalf("second page = %#v, cursor = %q", second, cursor)
	}
}

func integrationMinIOClient(t *testing.T) *minio.Client {
	t.Helper()
	return integrationClient(t, "CATALOG_MINIO_ACCESS_KEY_FILE", "CATALOG_MINIO_SECRET_KEY_FILE")
}

func integrationClient(t *testing.T, accessVariable, secretVariable string) *minio.Client {
	t.Helper()
	endpoint := os.Getenv("CATALOG_MINIO_ENDPOINT")
	bucket := os.Getenv("CATALOG_MINIO_BUCKET")
	accessKey := readIntegrationSecret(t, accessVariable)
	secretKey := readIntegrationSecret(t, secretVariable)
	if endpoint == "" || bucket == "" {
		t.Fatal("MinIO integration configuration is incomplete")
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: !integrationBool(t, "CATALOG_MINIO_INSECURE", true),
	})
	if err != nil {
		t.Fatal("MinIO integration client could not be created")
	}
	return client
}

func assertAccessDenied(t *testing.T, client *minio.Client, bucket, key string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err == nil || minio.ToErrorResponse(err).StatusCode != 403 {
		t.Fatalf("credential unexpectedly accessed bucket %q", bucket)
	}
}

func integrationBool(t *testing.T, variable string, fallback bool) bool {
	t.Helper()
	value := os.Getenv(variable)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		t.Fatalf("%s must be a boolean", variable)
	}
	return parsed
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
