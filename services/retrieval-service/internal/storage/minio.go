// Package storage implements private artifact reads from S3-compatible storage.
package storage

import (
	"context"
	"errors"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type MinIO struct {
	client *minio.Client
	bucket string
}

func NewMinIO(endpoint, accessKey, secretKey, bucket string, secure bool) (*MinIO, error) {
	if endpoint == "" || accessKey == "" || secretKey == "" || bucket == "" {
		return nil, errors.New("invalid artifact storage configuration")
	}
	client, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: secure})
	if err != nil {
		return nil, errors.New("configure artifact storage")
	}
	return &MinIO{client: client, bucket: bucket}, nil
}

func NewAWS(region, bucket string) (*MinIO, error) {
	if region == "" || bucket == "" {
		return nil, errors.New("invalid AWS artifact storage configuration")
	}
	client, err := minio.New("s3."+region+".amazonaws.com", &minio.Options{Creds: credentials.NewIAM(""), Secure: true, Region: region})
	if err != nil {
		return nil, errors.New("configure AWS artifact storage")
	}
	return &MinIO{client: client, bucket: bucket}, nil
}

func (m *MinIO) Open(ctx context.Context, reference string) (io.ReadCloser, int64, error) {
	object, err := m.client.GetObject(ctx, m.bucket, reference, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, errors.New("artifact unavailable")
	}
	information, err := object.Stat()
	if err != nil {
		_ = object.Close()
		return nil, 0, errors.New("artifact unavailable")
	}
	return object, information.Size, nil
}

func (m *MinIO) ReadBounded(ctx context.Context, reference string, maximum int64) ([]byte, error) {
	object, size, err := m.Open(ctx, reference)
	if err != nil {
		return nil, err
	}
	defer func() { _ = object.Close() }()
	if size < 1 || size > maximum {
		return nil, errors.New("artifact exceeds limit")
	}
	contents, err := io.ReadAll(io.LimitReader(object, maximum+1))
	if err != nil || int64(len(contents)) != size {
		return nil, errors.New("artifact read failed")
	}
	return contents, nil
}
