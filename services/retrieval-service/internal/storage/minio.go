// Package storage implements private artifact reads from S3-compatible storage.
package storage

import (
	"context"
	"errors"
	"io"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ObjectStore is the bounded private-artifact contract shared by local and AWS adapters.
type ObjectStore interface {
	Open(context.Context, string) (io.ReadCloser, int64, error)
	ReadBounded(context.Context, string, int64) ([]byte, error)
}

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

type s3Client interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// AWS uses the AWS SDK default credential chain, including Lambda execution-role credentials.
type AWS struct {
	client s3Client
	bucket string
}

func NewAWS(ctx context.Context, region, bucket string) (*AWS, error) {
	if region == "" || bucket == "" {
		return nil, errors.New("invalid AWS artifact storage configuration")
	}
	configuration, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, errors.New("configure AWS artifact storage")
	}
	return NewAWSWithClient(s3.NewFromConfig(configuration), bucket)
}

func NewAWSWithClient(client s3Client, bucket string) (*AWS, error) {
	if client == nil || bucket == "" {
		return nil, errors.New("invalid AWS artifact storage configuration")
	}
	return &AWS{client: client, bucket: bucket}, nil
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

func (a *AWS) Open(ctx context.Context, reference string) (io.ReadCloser, int64, error) {
	if reference == "" {
		return nil, 0, errors.New("artifact unavailable")
	}
	object, err := a.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &a.bucket, Key: &reference})
	if err != nil || object.Body == nil || object.ContentLength == nil {
		return nil, 0, errors.New("artifact unavailable")
	}
	return object.Body, *object.ContentLength, nil
}

func (a *AWS) ReadBounded(ctx context.Context, reference string, maximum int64) ([]byte, error) {
	object, size, err := a.Open(ctx, reference)
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
