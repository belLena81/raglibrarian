// Package artifact encodes deterministic chunk shards and a final manifest commit marker.
package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"time"

	ingestionv1 "github.com/belLena81/raglibrarian/pkg/proto/ingestion/v1"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	ErrArtifactConflict = errors.New("artifact integrity conflict")
	ErrArtifactLimit    = errors.New("artifact limit exceeded")
)

type Store interface {
	Put(context.Context, string, []byte, [32]byte) error
	Delete(context.Context, string) error
}

type Versions struct{ Extraction, Normalization, Tokenizer, Chunking string }
type Metadata struct {
	BookID       string
	SourceSHA256 [32]byte
	ConfigDigest [32]byte
	GeneratedAt  time.Time
}
type Limits struct {
	ChunksPerShard       int
	MaximumShardBytes    int
	MaximumManifestBytes int
}
type Result struct {
	ManifestReference string
	ManifestSHA256    [32]byte
	ManifestByteSize  int64
	PageCount         uint32
	ChunkCount        uint32
}

type Writer struct {
	store       Store
	metadata    Metadata
	versions    Versions
	limits      Limits
	prefix      string
	pending     []*ingestionv1.ChunkV1
	pendingSize int
	descriptors []*ingestionv1.ChunkShardDescriptorV1
	chunkCount  uint32
	finalized   bool
	written     []string
}

func NewWriter(store Store, metadata Metadata, versions Versions, limits Limits) (*Writer, error) {
	if store == nil || metadata.BookID == "" || metadata.GeneratedAt.IsZero() || versions.Extraction == "" || versions.Normalization == "" || versions.Tokenizer == "" || versions.Chunking == "" || limits.ChunksPerShard < 1 || limits.MaximumShardBytes < 1024 || limits.MaximumManifestBytes < 1024 {
		return nil, errors.New("invalid artifact writer configuration")
	}
	prefix := path.Join("books", metadata.BookID, hex.EncodeToString(metadata.SourceSHA256[:]), hex.EncodeToString(metadata.ConfigDigest[:]))
	return &Writer{store: store, metadata: metadata, versions: versions, limits: limits, prefix: prefix, pending: make([]*ingestionv1.ChunkV1, 0, limits.ChunksPerShard)}, nil
}

func (w *Writer) Add(ctx context.Context, value domain.Chunk) error {
	if w.finalized {
		return errors.New("artifact writer already finalized")
	}
	contentSHA256 := value.ContentSHA256()
	chunk := &ingestionv1.ChunkV1{ChunkId: value.ID(), BookId: value.BookID(), Order: value.Order(), Text: value.Text(), ContentSha256: contentSHA256[:], Chapter: value.Chapter(), Section: value.Section(), PageStart: value.PageStart(), PageEnd: value.PageEnd(), TokenStart: value.TokenStart(), TokenEnd: value.TokenEnd(), ExtractionVersion: w.versions.Extraction, NormalizationVersion: w.versions.Normalization, TokenizerVersion: w.versions.Tokenizer, ChunkingVersion: w.versions.Chunking}
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(chunk)
	if err != nil {
		return errors.New("encode chunk")
	}
	recordSize := protowire.SizeVarint(uint64(len(encoded))) + len(encoded)
	if recordSize > w.limits.MaximumShardBytes {
		return ErrArtifactLimit
	}
	if len(w.pending) > 0 && (len(w.pending) >= w.limits.ChunksPerShard || w.pendingSize+recordSize > w.limits.MaximumShardBytes) {
		if err = w.flush(ctx); err != nil {
			return err
		}
	}
	w.pending = append(w.pending, chunk)
	w.pendingSize += recordSize
	w.chunkCount++
	return nil
}

func (w *Writer) Finalize(ctx context.Context, pageCount uint32) (Result, error) {
	if w.finalized || pageCount == 0 || w.chunkCount == 0 {
		return Result{}, errors.New("cannot finalize artifacts")
	}
	if err := w.flush(ctx); err != nil {
		return Result{}, err
	}
	manifest := &ingestionv1.ChunkManifestV1{SchemaVersion: "v1", BookId: w.metadata.BookID, SourceSha256: append([]byte(nil), w.metadata.SourceSHA256[:]...), ProcessingConfigDigest: append([]byte(nil), w.metadata.ConfigDigest[:]...), ExtractionVersion: w.versions.Extraction, NormalizationVersion: w.versions.Normalization, TokenizerVersion: w.versions.Tokenizer, ChunkingVersion: w.versions.Chunking, PageCount: pageCount, ChunkCount: w.chunkCount, GeneratedAt: timestamppb.New(w.metadata.GeneratedAt), Shards: w.descriptors}
	contents, err := proto.MarshalOptions{Deterministic: true}.Marshal(manifest)
	if err != nil || len(contents) > w.limits.MaximumManifestBytes {
		return Result{}, ErrArtifactLimit
	}
	reference := path.Join(w.prefix, "manifest.pb")
	sum := sha256.Sum256(contents)
	if err = w.store.Put(ctx, reference, contents, sum); err != nil {
		return Result{}, err
	}
	w.finalized = true
	w.written = append(w.written, reference)
	return Result{ManifestReference: reference, ManifestSHA256: sum, ManifestByteSize: int64(len(contents)), PageCount: pageCount, ChunkCount: w.chunkCount}, nil
}

func (w *Writer) flush(ctx context.Context) error {
	if len(w.pending) == 0 {
		return nil
	}
	uncompressed := make([]byte, 0, w.pendingSize)
	for _, chunk := range w.pending {
		encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(chunk)
		if err != nil {
			return errors.New("encode shard")
		}
		uncompressed = protowire.AppendVarint(uncompressed, uint64(len(encoded)))
		uncompressed = append(uncompressed, encoded...)
	}
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return errors.New("initialize shard compressor")
	}
	compressed := encoder.EncodeAll(uncompressed, nil)
	if err = encoder.Close(); err != nil {
		return errors.New("close shard compressor")
	}
	sum := sha256.Sum256(compressed)
	index := len(w.descriptors)
	reference := path.Join(w.prefix, "shards", fmt.Sprintf("%06d.pb.zst", index))
	if err = w.store.Put(ctx, reference, compressed, sum); err != nil {
		return err
	}
	chunkCount := uint32(len(w.pending)) // #nosec G115 -- pending chunks are bounded to 256.
	w.descriptors = append(w.descriptors, &ingestionv1.ChunkShardDescriptorV1{
		Reference:            reference,
		Sha256:               append([]byte(nil), sum[:]...),
		CompressedByteSize:   int64(len(compressed)),
		UncompressedByteSize: int64(len(uncompressed)),
		ChunkCount:           chunkCount,
		FirstChunkOrder:      w.pending[0].Order,
		LastChunkOrder:       w.pending[len(w.pending)-1].Order,
	})
	w.written = append(w.written, reference)
	w.pending = w.pending[:0]
	w.pendingSize = 0
	return nil
}

func (w *Writer) Abort(ctx context.Context) error {
	if w.finalized {
		return nil
	}
	var result error
	for _, reference := range w.written {
		result = errors.Join(result, w.store.Delete(ctx, reference))
	}
	w.written = nil
	return result
}
