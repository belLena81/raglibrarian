package repository

import (
	"context"
	"errors"
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

func (s *MinIOObjectStore) ListCompleted(ctx context.Context, prefix, cursor string, limit int) ([]catalog.StoredObject, string, error) {
	if prefix != "originals/" || limit < 1 || limit > 100 {
		return nil, "", errors.New("invalid reconciliation listing boundary")
	}
	objects := make([]catalog.StoredObject, 0, limit)
	for object := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:     prefix,
		Recursive:  true,
		MaxKeys:    limit,
		StartAfter: cursor,
	}) {
		if object.Err != nil {
			return nil, "", fmt.Errorf("list original objects: %w", object.Err)
		}
		objects = append(objects, catalog.StoredObject{Reference: object.Key, Size: object.Size, LastModified: object.LastModified})
	}
	next := ""
	if len(objects) == limit {
		next = objects[len(objects)-1].Reference
	}
	return objects, next, nil
}

var _ catalog.OriginalObjectStore = (*MinIOObjectStore)(nil)
var _ catalog.ReconciliationObjectStore = (*MinIOObjectStore)(nil)
