// Package storage implements read-only source and write-only artifact object adapters.
package storage

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/artifact"
	"github.com/minio/minio-go/v7"
)

type SourceStore struct {
	client *minio.Client
	bucket string
}

func NewSourceStore(client *minio.Client, bucket string) *SourceStore {
	if client == nil || bucket == "" {
		panic("ingestion source store requires client and bucket")
	}
	return &SourceStore{client: client, bucket: bucket}
}

func (s *SourceStore) Open(ctx context.Context, reference string) (io.ReadCloser, int64, error) {
	object, err := s.client.GetObject(ctx, s.bucket, reference, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, errors.New("source unavailable")
	}
	info, err := object.Stat()
	if err != nil {
		_ = object.Close()
		return nil, 0, errors.New("source unavailable")
	}
	return object, info.Size, nil
}

func (s *SourceStore) Ready(ctx context.Context) bool {
	return prefixReady(ctx, s.client, s.bucket, "originals/")
}

type ArtifactStore struct {
	client *minio.Client
	bucket string
}

func (s *ArtifactStore) Ready(ctx context.Context) bool {
	return prefixReady(ctx, s.client, s.bucket, "books/")
}

func NewArtifactStore(client *minio.Client, bucket string) *ArtifactStore {
	if client == nil || bucket == "" {
		panic("ingestion artifact store requires client and bucket")
	}
	return &ArtifactStore{client: client, bucket: bucket}
}

func (s *ArtifactStore) Put(ctx context.Context, reference string, contents []byte, checksum [32]byte) error {
	if !validArtifactReference(reference) || len(contents) == 0 {
		return errors.New("invalid artifact")
	}
	expected := hex.EncodeToString(checksum[:])
	if info, err := s.client.StatObject(ctx, s.bucket, reference, minio.StatObjectOptions{}); err == nil {
		if info.Size == int64(len(contents)) && metadataValue(info.UserMetadata, "sha256") == expected {
			return nil
		}
		return artifact.ErrArtifactConflict
	}
	_, err := s.client.PutObject(ctx, s.bucket, reference, bytes.NewReader(contents), int64(len(contents)), minio.PutObjectOptions{ContentType: contentType(reference), UserMetadata: map[string]string{"sha256": expected}})
	if err != nil {
		return errors.New("artifact storage unavailable")
	}
	info, err := s.client.StatObject(ctx, s.bucket, reference, minio.StatObjectOptions{})
	if err != nil || info.Size != int64(len(contents)) || metadataValue(info.UserMetadata, "sha256") != expected {
		return errors.New("artifact receipt mismatch")
	}
	return nil
}

func (s *ArtifactStore) Delete(ctx context.Context, reference string) error {
	if !validArtifactReference(reference) {
		return errors.New("invalid artifact reference")
	}
	if err := s.client.RemoveObject(ctx, s.bucket, reference, minio.RemoveObjectOptions{}); err != nil {
		return errors.New("artifact cleanup unavailable")
	}
	return nil
}

func (s *ArtifactStore) DeletePrefix(ctx context.Context, prefix string) error {
	if !validArtifactPrefix(prefix) {
		return errors.New("invalid artifact prefix")
	}
	for object := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if object.Err != nil {
			return errors.New("artifact cleanup unavailable")
		}
		if !validArtifactReference(object.Key) || !strings.HasPrefix(object.Key, prefix) {
			return errors.New("artifact cleanup boundary violated")
		}
		if err := s.client.RemoveObject(ctx, s.bucket, object.Key, minio.RemoveObjectOptions{}); err != nil {
			return errors.New("artifact cleanup unavailable")
		}
	}
	return nil
}

func metadataValue(values map[string]string, key string) string {
	for name, value := range values {
		if strings.EqualFold(strings.TrimPrefix(name, "x-amz-meta-"), key) {
			return value
		}
	}
	return ""
}

func prefixReady(ctx context.Context, client *minio.Client, bucket, prefix string) bool {
	// A prefix-scoped list works with the runtime's least-privilege ListBucket
	// condition. It reads no object body and returns at most one metadata entry.
	for object := range client.ListObjects(ctx, bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
		MaxKeys:   1,
	}) {
		return object.Err == nil
	}
	return ctx.Err() == nil
}

func validArtifactReference(reference string) bool {
	prefix, name, ok := splitArtifactReference(reference)
	if !ok || !validArtifactPrefix(prefix) {
		return false
	}
	if name == "manifest.pb" {
		return true
	}
	if !strings.HasPrefix(name, "shards/") || !strings.HasSuffix(name, ".pb.zst") {
		return false
	}
	index := strings.TrimSuffix(strings.TrimPrefix(name, "shards/"), ".pb.zst")
	if len(index) != 6 {
		return false
	}
	for _, char := range index {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func validArtifactPrefix(prefix string) bool {
	parts := strings.Split(prefix, "/")
	if len(parts) != 5 || parts[0] != "books" || parts[4] != "" || !validBookID(parts[1]) {
		return false
	}
	return validLowerHexDigest(parts[2]) && validLowerHexDigest(parts[3])
}

func splitArtifactReference(reference string) (string, string, bool) {
	parts := strings.Split(reference, "/")
	if len(parts) == 5 {
		return strings.Join(parts[:4], "/") + "/", parts[4], true
	}
	if len(parts) == 6 {
		return strings.Join(parts[:4], "/") + "/", strings.Join(parts[4:], "/"), true
	}
	return "", "", false
}

func validBookID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func validLowerHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func contentType(reference string) string {
	if strings.HasSuffix(reference, "manifest.pb") {
		return "application/x-protobuf"
	}
	return "application/zstd"
}

func (s *ArtifactStore) String() string { return fmt.Sprintf("ArtifactStore{bucket:%q}", s.bucket) }
