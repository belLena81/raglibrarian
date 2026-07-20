package storage

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestAWSReadBoundedUsesS3ObjectMetadata(t *testing.T) {
	contents := []byte("artifact")
	client := &stubS3Client{output: &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(contents)), ContentLength: ptr(int64(len(contents)))}}
	store, err := NewAWSWithClient(client, "artifact-bucket")
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadBounded(context.Background(), "books/book-1/shard", 64)
	if err != nil || string(got) != string(contents) {
		t.Fatalf("ReadBounded() = %q, %v", got, err)
	}
	if client.bucket != "artifact-bucket" || client.key != "books/book-1/shard" {
		t.Fatalf("GetObject() target = %q/%q", client.bucket, client.key)
	}
}

func TestAWSReadBoundedRejectsMissingOrOversizedObjects(t *testing.T) {
	store, _ := NewAWSWithClient(&stubS3Client{output: &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader([]byte("too large"))), ContentLength: ptr(int64(9))}}, "artifact-bucket")
	if _, err := store.ReadBounded(context.Background(), "books/book-1/shard", 8); err == nil {
		t.Fatal("ReadBounded() error = nil")
	}
}

type stubS3Client struct {
	output      *s3.GetObjectOutput
	err         error
	bucket, key string
}

func (s *stubS3Client) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	s.bucket = value(input.Bucket)
	s.key = value(input.Key)
	return s.output, s.err
}

func ptr(value int64) *int64 { return &value }
func value(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}
