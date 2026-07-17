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

func (s *MinIOObjectStore) Put(ctx context.Context, key string, reader io.Reader) (catalog.ObjectReceipt, error) {
	receipt, err := s.client.PutObject(ctx, s.bucket, key, reader, -1, minio.PutObjectOptions{
		ContentType: "application/pdf",
		PartSize:    5 << 20,
		Checksum:    minio.ChecksumCRC32C,
	})
	if err != nil {
		return catalog.ObjectReceipt{}, fmt.Errorf("put original: %w", err)
	}
	stored, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{Checksum: true})
	if err != nil {
		return catalog.ObjectReceipt{}, fmt.Errorf("verify original: %w", err)
	}
	if stored.Size != receipt.Size || receipt.ChecksumCRC32C == "" || stored.ChecksumCRC32C != receipt.ChecksumCRC32C {
		return catalog.ObjectReceipt{}, fmt.Errorf("verify original: receipt mismatch")
	}
	return catalog.ObjectReceipt{Size: stored.Size, ChecksumCRC32C: stored.ChecksumCRC32C}, nil
}

func (s *MinIOObjectStore) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete original: %w", err)
	}
	return nil
}

var _ catalog.OriginalObjectStore = (*MinIOObjectStore)(nil)
