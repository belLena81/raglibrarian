package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
	"testing"

	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
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

func TestReaderAcceptsValidShardSmallerThanZstdMinimumWindow(t *testing.T) {
	uncompressed := validTinyShardUncompressed(t)
	compressed := compressedShard(t, uncompressed)
	reader, err := NewReader(&stubObjectReader{object: io.NopCloser(bytes.NewReader(compressed)), size: int64(len(compressed))})
	if err != nil {
		t.Fatal(err)
	}

	chunks, err := reader.ReadShard(context.Background(), application.BatchWork{
		ShardReference:    "books/book-1/shards/000000.pb.zst",
		ShardSHA256:       sha256.Sum256(compressed),
		CompressedBytes:   int64(len(compressed)),
		UncompressedBytes: int64(len(uncompressed)),
		ChunkCount:        1,
	})

	if err != nil {
		t.Fatalf("ReadShard() error = %v", err)
	}
	if len(chunks) != 1 || chunks[0].Text != "Tiny evidence" {
		t.Fatalf("chunks = %#v", chunks)
	}
	if len(uncompressed) >= zstd.MinWindowSize {
		t.Fatalf("test shard is not below zstd minimum window: %d", len(uncompressed))
	}
}

func TestReaderRetainsChunkContractFields(t *testing.T) {
	reader, err := NewReader(validShardObjectReader(t))
	if err != nil {
		t.Fatal(err)
	}

	chunks, err := reader.ReadShard(context.Background(), application.BatchWork{
		ShardReference:       "books/book-1/shards/000000.pb.zst",
		ShardSHA256:          validShardDigest(t),
		CompressedBytes:      int64(len(validShardCompressed(t))),
		UncompressedBytes:    int64(len(validShardUncompressed(t))),
		ChunkCount:           1,
		ManifestPageCount:    2,
		FirstChunkOrder:      7,
		LastChunkOrder:       7,
		ExtractionVersion:    "poppler-layout-v1",
		NormalizationVersion: "nfc-v1",
		TokenizerVersion:     "cl100k_base-v1",
		ChunkingVersion:      "token-window-v2",
		StructureVersion:     "heading-carry-v1",
		MaximumTokens:        800,
		OverlapTokens:        120,
	})
	if err != nil {
		t.Fatalf("ReadShard() error = %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
	chunk := chunks[0]
	if chunk.Order != 7 || chunk.TokenStart != 13 || chunk.TokenEnd != 21 || chunk.ExtractionVersion != "poppler-layout-v1" || chunk.ChunkingVersion != "token-window-v2" {
		t.Fatalf("chunk = %#v", chunk)
	}
}

func validShardObjectReader(t *testing.T) *stubObjectReader {
	t.Helper()
	compressed := validShardCompressed(t)
	return &stubObjectReader{object: io.NopCloser(bytes.NewReader(compressed)), size: int64(len(compressed))}
}

func validShardCompressed(t *testing.T) []byte {
	t.Helper()
	return compressedShard(t, validShardUncompressed(t))
}

func compressedShard(t *testing.T, uncompressed []byte) []byte {
	t.Helper()
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = encoder.Close() })
	return encoder.EncodeAll(uncompressed, nil)
}

func validShardDigest(t *testing.T) [32]byte {
	t.Helper()
	return sha256.Sum256(validShardCompressed(t))
}

func validShardUncompressed(t *testing.T) []byte {
	t.Helper()
	text := strings.Repeat("Evidence ", 128)
	return chunkRecord(t, &ingestionv1.ChunkV1{
		ChunkId:              "chunk-1",
		BookId:               "book-1",
		Order:                7,
		Text:                 text,
		ContentSha256:        digestBytes(text),
		PageStart:            1,
		PageEnd:              2,
		TokenStart:           13,
		TokenEnd:             21,
		ExtractionVersion:    "poppler-layout-v1",
		NormalizationVersion: "nfc-v1",
		TokenizerVersion:     "cl100k_base-v1",
		ChunkingVersion:      "token-window-v2",
		StructureVersion:     "heading-carry-v1",
	})
}

func validTinyShardUncompressed(t *testing.T) []byte {
	t.Helper()
	text := "Tiny evidence"
	return chunkRecord(t, &ingestionv1.ChunkV1{
		ChunkId:       "chunk-1",
		BookId:        "book-1",
		Order:         0,
		Text:          text,
		ContentSha256: digestBytes(text),
		PageStart:     1,
		PageEnd:       1,
		TokenStart:    0,
		TokenEnd:      2,
	})
}

func chunkRecord(t *testing.T, message *ingestionv1.ChunkV1) []byte {
	t.Helper()
	record, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	prefix := protowire.AppendVarint(nil, uint64(len(record)))
	return append(prefix, record...)
}

func digestBytes(text string) []byte {
	digest := sha256.Sum256([]byte(text))
	return digest[:]
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
