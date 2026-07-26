package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type stubS3API struct {
	getObject func(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

func (s *stubS3API) GetObject(ctx context.Context, input *s3.GetObjectInput, options ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if s.getObject != nil {
		return s.getObject(ctx, input, options...)
	}
	return nil, errors.New("unexpected GetObject")
}

func (s *stubS3API) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return nil, errors.New("unexpected HeadObject")
}

func (s *stubS3API) HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return nil, errors.New("unexpected HeadBucket")
}

func (s *stubS3API) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return nil, errors.New("unexpected PutObject")
}

func (s *stubS3API) DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return nil, errors.New("unexpected DeleteObject")
}

func (s *stubS3API) ListObjectVersions(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	return nil, errors.New("unexpected ListObjectVersions")
}

func TestAWSSourceStoreOpenAcceptsPDFAndEPUBReferences(t *testing.T) {
	client := &stubS3API{getObject: func(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
		payload := []byte("content:" + *input.Key)
		return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(payload)), ContentLength: int64Ptr(int64(len(payload)))}, nil
	}}
	store, err := NewAWSSourceStore(client, "original-books")
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range []string{"originals/book.pdf", "originals/book.epub"} {
		t.Run(reference, func(t *testing.T) {
			body, size, err := store.Open(context.Background(), reference)
			if err != nil {
				t.Fatal(err)
			}
			defer body.Close()
			if size == 0 {
				t.Fatal("size should be reported")
			}
			contents, readErr := io.ReadAll(body)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(contents) != "content:"+reference {
				t.Fatalf("contents = %q", contents)
			}
		})
	}
}

func TestAWSSourceStoreOpenRejectsUnsafeReferences(t *testing.T) {
	store, err := NewAWSSourceStore(&stubS3API{}, "original-books")
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range []string{"originals/nested/book.epub", "../originals/book.epub"} {
		t.Run(reference, func(t *testing.T) {
			body, size, err := store.Open(context.Background(), reference)
			if err == nil {
				if body != nil {
					_ = body.Close()
				}
				t.Fatalf("Open(%q) succeeded with size %d", reference, size)
			}
		})
	}
}
