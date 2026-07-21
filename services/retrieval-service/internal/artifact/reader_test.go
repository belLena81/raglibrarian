package artifact

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
)

func TestReaderMarksObjectOpenErrorsUnavailable(t *testing.T) {
	reader, err := NewReader(&stubObjectReader{err: context.DeadlineExceeded})
	if err != nil {
		t.Fatal(err)
	}

	_, err = reader.ReadShard(context.Background(), application.BatchWork{ShardReference: "books/book-1/shards/000000.pb.zst"})

	if !errors.Is(err, application.ErrArtifactUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ReadShard() error = %v, want artifact unavailable deadline", err)
	}
}

func TestReaderMarksStreamReadErrorsUnavailable(t *testing.T) {
	reader, err := NewReader(&stubObjectReader{object: io.NopCloser(errorReader{}), size: 10})
	if err != nil {
		t.Fatal(err)
	}

	_, err = reader.ReadShard(context.Background(), application.BatchWork{ShardReference: "books/book-1/shards/000000.pb.zst", CompressedBytes: 10})

	if !errors.Is(err, application.ErrArtifactUnavailable) {
		t.Fatalf("ReadShard() error = %v, want artifact unavailable", err)
	}
}

func TestReaderLeavesCompressedIntegrityErrorsTerminal(t *testing.T) {
	reader, err := NewReader(&stubObjectReader{object: io.NopCloser(strings.NewReader("not-zstd")), size: 8})
	if err != nil {
		t.Fatal(err)
	}

	_, err = reader.ReadShard(context.Background(), application.BatchWork{ShardReference: "books/book-1/shards/000000.pb.zst", CompressedBytes: 8})

	if err == nil || errors.Is(err, application.ErrArtifactUnavailable) {
		t.Fatalf("ReadShard() error = %v, want deterministic integrity error", err)
	}
}

type stubObjectReader struct {
	object io.ReadCloser
	size   int64
	err    error
}

func (s *stubObjectReader) Open(context.Context, string) (io.ReadCloser, int64, error) {
	return s.object, s.size, s.err
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("stream unavailable")
}
