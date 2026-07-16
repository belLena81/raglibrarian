package repository

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"

	"github.com/belLena81/raglibrarian/services/catalog-service/internal/catalog"
)

// MinIOObjectStore stores original uploads in a pre-provisioned private bucket.
type MinIOObjectStore struct {
	client *minio.Client
	bucket string
}

func NewMinIOObjectStore(client *minio.Client, bucket string) *MinIOObjectStore {
	if client == nil || bucket == "" {
		panic("repository: MinIO client and bucket are required")
	}
	return &MinIOObjectStore{client: client, bucket: bucket}
}

func (s *MinIOObjectStore) Put(ctx context.Context, key string, reader io.Reader) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, reader, -1, minio.PutObjectOptions{
		ContentType:    "application/pdf",
		SendContentMd5: false,
	})
	if err != nil {
		return fmt.Errorf("put original: %w", err)
	}
	return nil
}

func (s *MinIOObjectStore) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete original: %w", err)
	}
	return nil
}

var _ catalog.OriginalObjectStore = (*MinIOObjectStore)(nil)
