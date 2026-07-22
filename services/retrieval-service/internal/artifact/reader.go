// Package artifact verifies and decodes bounded Ingestion artifacts.
package artifact

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"

	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/application"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

type ObjectReader interface {
	Open(context.Context, string) (io.ReadCloser, int64, error)
}

type Reader struct {
	objects ObjectReader
}

func NewReader(objects ObjectReader) (*Reader, error) {
	if objects == nil {
		return nil, errors.New("artifact object reader is required")
	}
	return &Reader{objects: objects}, nil
}

func (r *Reader) ReadShard(ctx context.Context, work application.BatchWork) ([]application.Chunk, error) {
	object, size, err := r.objects.Open(ctx, work.ShardReference)
	if err != nil {
		return nil, errors.Join(errors.New("open shard"), application.ErrArtifactUnavailable, err)
	}
	defer func() { _ = object.Close() }()
	if size != work.CompressedBytes || size > 32<<20 {
		return nil, errors.New("invalid compressed shard size")
	}
	compressed, err := io.ReadAll(io.LimitReader(object, work.CompressedBytes+1))
	if err != nil {
		return nil, errors.Join(errors.New("read shard"), application.ErrArtifactUnavailable, err)
	}
	if int64(len(compressed)) != work.CompressedBytes || sha256.Sum256(compressed) != work.ShardSHA256 {
		return nil, errors.New("invalid compressed shard integrity")
	}
	if work.UncompressedBytes < 1 {
		return nil, errors.New("invalid uncompressed shard")
	}
	decoderMemory := uint64(work.UncompressedBytes + 1) // #nosec G115 -- BatchWork.Validate caps the positive size at 64 MiB.
	if decoderMemory < zstd.MinWindowSize {
		decoderMemory = zstd.MinWindowSize
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(decoderMemory))
	if err != nil {
		return nil, errors.New("initialize shard decoder")
	}
	defer decoder.Close()
	uncompressed, err := decoder.DecodeAll(compressed, make([]byte, 0, work.UncompressedBytes))
	if err != nil || int64(len(uncompressed)) != work.UncompressedBytes {
		return nil, errors.New("invalid uncompressed shard")
	}
	chunks := make([]application.Chunk, 0, work.ChunkCount)
	for len(uncompressed) > 0 {
		length, consumed := protowire.ConsumeVarint(uncompressed)
		if consumed < 0 || length == 0 || length > uint64(len(uncompressed)-consumed) { // #nosec G115 -- shard storage is capped at 64 MiB.
			return nil, errors.New("invalid shard record")
		}
		uncompressed = uncompressed[consumed:]
		var message ingestionv1.ChunkV1
		recordLength := int(length) // #nosec G115 -- length was checked against the bounded in-memory shard above.
		if err = (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(uncompressed[:recordLength], &message); err != nil || len(message.ContentSha256) != sha256.Size {
			return nil, errors.New("invalid chunk record")
		}
		var contentDigest [sha256.Size]byte
		copy(contentDigest[:], message.ContentSha256)
		chunks = append(chunks, application.Chunk{ChunkID: message.ChunkId, BookID: message.BookId, Order: message.Order, Text: message.Text,
			Chapter: message.Chapter, Section: message.Section, ContentSHA256: contentDigest, PageStart: message.PageStart, PageEnd: message.PageEnd,
			TokenStart: message.TokenStart, TokenEnd: message.TokenEnd, ExtractionVersion: message.ExtractionVersion,
			NormalizationVersion: message.NormalizationVersion, TokenizerVersion: message.TokenizerVersion,
			ChunkingVersion: message.ChunkingVersion, StructureVersion: message.StructureVersion})
		uncompressed = uncompressed[recordLength:]
		if len(chunks) > int(work.ChunkCount) {
			return nil, errors.New("too many shard records")
		}
	}
	return chunks, nil
}
