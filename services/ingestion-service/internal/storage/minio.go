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

type ArtifactStore struct {
	client *minio.Client
	bucket string
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
	if !validArtifactReference(prefix) || !strings.HasSuffix(prefix, "/") {
		return errors.New("invalid artifact prefix")
	}
	for object := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if object.Err != nil {
			return errors.New("artifact cleanup unavailable")
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

func validArtifactReference(reference string) bool {
	return strings.HasPrefix(reference, "books/") && !strings.Contains(reference, "..") && !strings.ContainsAny(reference, "\\\x00") && len(reference) <= 1024
}

func contentType(reference string) string {
	if strings.HasSuffix(reference, "manifest.pb") {
		return "application/x-protobuf"
	}
	return "application/zstd"
}

func (s *ArtifactStore) String() string { return fmt.Sprintf("ArtifactStore{bucket:%q}", s.bucket) }
