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

type Versions struct{ Extraction, Normalization, Tokenizer, Chunking, Structure string }
type ProcessingProfile struct {
	MaximumTokens int
	OverlapTokens int
}
type ContentSelectionRange struct {
	Start  uint32
	End    uint32
	Reason string
}
type ContentSelection struct {
	Mode                    string
	SelectorVersion         string
	ParserVersion           string
	ModelDigest             [sha256.Size]byte
	PolicyDigest            [sha256.Size]byte
	ProcessingProfileDigest [sha256.Size]byte
	FallbackReason          string
	OriginalLocationCount   uint32
	Ranges                  []ContentSelectionRange
}
type Metadata struct {
	BookID           string
	SourceSHA256     [32]byte
	ConfigDigest     [32]byte
	GeneratedAt      time.Time
	LifecycleVersion int64
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
	profile     ProcessingProfile
	limits      Limits
	prefix      string
	pending     []encodedChunk
	pendingSize int
	descriptors []*ingestionv1.ChunkShardDescriptorV1
	chunkCount  uint32
	finalized   bool
	written     []string
	encoder     *zstd.Encoder
}

type encodedChunk struct {
	message *ingestionv1.ChunkV1
	bytes   []byte
}

func NewWriter(store Store, metadata Metadata, versions Versions, profile ProcessingProfile, limits Limits) (*Writer, error) {
	if store == nil || metadata.BookID == "" || metadata.GeneratedAt.IsZero() || metadata.LifecycleVersion < 1 || versions.Extraction == "" || versions.Normalization == "" || versions.Tokenizer == "" || versions.Chunking == "" || versions.Structure == "" || profile.MaximumTokens < 1 || profile.OverlapTokens < 0 || profile.OverlapTokens >= profile.MaximumTokens || limits.ChunksPerShard < 1 || limits.MaximumShardBytes < 1024 || limits.MaximumManifestBytes < 1024 {
		return nil, errors.New("invalid artifact writer configuration")
	}
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, errors.New("initialize shard compressor")
	}
	prefix := path.Join("books", metadata.BookID, hex.EncodeToString(metadata.SourceSHA256[:]), hex.EncodeToString(metadata.ConfigDigest[:]))
	return &Writer{store: store, metadata: metadata, versions: versions, profile: profile, limits: limits, prefix: prefix, pending: make([]encodedChunk, 0, limits.ChunksPerShard), encoder: encoder}, nil
}

func (w *Writer) Add(ctx context.Context, value domain.Chunk) error {
	if w.finalized {
		return errors.New("artifact writer already finalized")
	}
	contentSHA256 := value.ContentSHA256()
	chunk := &ingestionv1.ChunkV1{ChunkId: value.ID(), BookId: value.BookID(), Order: value.Order(), Text: value.Text(), ContentSha256: contentSHA256[:], Chapter: value.Chapter(), Section: value.Section(), PageStart: value.PageStart(), PageEnd: value.PageEnd(), TokenStart: value.TokenStart(), TokenEnd: value.TokenEnd(), ExtractionVersion: w.versions.Extraction, NormalizationVersion: w.versions.Normalization, TokenizerVersion: w.versions.Tokenizer, ChunkingVersion: w.versions.Chunking, StructureVersion: w.versions.Structure}
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
	w.pending = append(w.pending, encodedChunk{message: chunk, bytes: encoded})
	w.pendingSize += recordSize
	w.chunkCount++
	return nil
}

func (w *Writer) Finalize(ctx context.Context, pageCount uint32, contentSelection *ContentSelection) (Result, error) {
	if w.finalized || pageCount == 0 || w.chunkCount == 0 {
		return Result{}, errors.New("cannot finalize artifacts")
	}
	if err := w.flush(ctx); err != nil {
		return Result{}, err
	}
	selectionMessage, err := encodeContentSelection(contentSelection, pageCount, w.metadata.ConfigDigest)
	if err != nil {
		return Result{}, err
	}
	manifest := &ingestionv1.ChunkManifestV1{SchemaVersion: "v1", BookId: w.metadata.BookID, SourceSha256: append([]byte(nil), w.metadata.SourceSHA256[:]...), ProcessingConfigDigest: append([]byte(nil), w.metadata.ConfigDigest[:]...), ExtractionVersion: w.versions.Extraction, NormalizationVersion: w.versions.Normalization, TokenizerVersion: w.versions.Tokenizer, ChunkingVersion: w.versions.Chunking, StructureVersion: w.versions.Structure, MaximumTokens: uint32(w.profile.MaximumTokens), OverlapTokens: uint32(w.profile.OverlapTokens), PageCount: pageCount, ChunkCount: w.chunkCount, GeneratedAt: timestamppb.New(w.metadata.GeneratedAt), Shards: w.descriptors, LifecycleVersion: w.metadata.LifecycleVersion, ContentSelection: selectionMessage} // #nosec G115 -- validated positive processing bounds.
	contents, err := proto.MarshalOptions{Deterministic: true}.Marshal(manifest)
	if err != nil || len(contents) > w.limits.MaximumManifestBytes {
		return Result{}, ErrArtifactLimit
	}
	reference := path.Join(w.prefix, "manifest.pb")
	sum := sha256.Sum256(contents)
	if err = w.store.Put(ctx, reference, contents, sum); err != nil {
		return Result{}, err
	}
	w.written = append(w.written, reference)
	if err = w.encoder.Close(); err != nil {
		return Result{}, errors.New("close artifact compressor")
	}
	w.finalized = true
	return Result{ManifestReference: reference, ManifestSHA256: sum, ManifestByteSize: int64(len(contents)), PageCount: pageCount, ChunkCount: w.chunkCount}, nil
}

func encodeContentSelection(value *ContentSelection, pageCount uint32, configDigest [sha256.Size]byte) (*ingestionv1.ContentSelectionV1, error) {
	if value == nil {
		return nil, nil
	}
	mode, modeOK := contentSelectionMode(value.Mode)
	fallback, fallbackOK := contentSelectionFallback(value.FallbackReason)
	if !modeOK || !fallbackOK || !safeAuditValue(value.SelectorVersion) || !safeAuditValue(value.ParserVersion) ||
		value.ModelDigest == ([sha256.Size]byte{}) || value.PolicyDigest == ([sha256.Size]byte{}) ||
		value.ProcessingProfileDigest != configDigest {
		return nil, errors.New("invalid content selection audit")
	}
	originalLocationCount := value.OriginalLocationCount
	if originalLocationCount == 0 && fallback != ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_NONE && len(value.Ranges) == 0 {
		originalLocationCount = pageCount
	}
	if originalLocationCount != pageCount {
		return nil, errors.New("invalid content selection ordinal count")
	}
	if (fallback != ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_NONE && len(value.Ranges) != 0) ||
		(mode == ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_OBSERVATION && fallback != ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_OBSERVATION) {
		return nil, errors.New("invalid content selection fallback audit")
	}
	ranges := make([]*ingestionv1.ExcludedRangeV1, 0, len(value.Ranges))
	var excluded uint64
	var previousEnd uint32
	for index, item := range value.Ranges {
		reason, ok := contentSelectionReason(item.Reason)
		if !ok || item.Start == 0 || item.End < item.Start || item.End > pageCount || (index > 0 && item.Start <= previousEnd) {
			return nil, errors.New("invalid content selection audit range")
		}
		excluded += uint64(item.End) - uint64(item.Start) + 1
		previousEnd = item.End
		ranges = append(ranges, &ingestionv1.ExcludedRangeV1{StartOrdinal: item.Start, EndOrdinal: item.End, Reason: reason})
	}
	if excluded > uint64(pageCount) {
		return nil, errors.New("invalid content selection audit count")
	}
	excludedCount := uint32(excluded) // #nosec G115 -- excluded locations are bounded by pageCount.
	return &ingestionv1.ContentSelectionV1{
		Mode: mode, SelectorVersion: value.SelectorVersion, ParserVersion: value.ParserVersion,
		ModelDigest: append([]byte(nil), value.ModelDigest[:]...), PolicyDigest: append([]byte(nil), value.PolicyDigest[:]...),
		FallbackReason: fallback, OriginalOrdinalCount: pageCount, RetainedOrdinalCount: pageCount - excludedCount,
		ExcludedOrdinalCount: excludedCount, ExcludedRatio: float64(excludedCount) / float64(pageCount),
		ExcludedRanges: ranges, ProcessingProfileDigest: append([]byte(nil), configDigest[:]...),
	}, nil
}

func contentSelectionMode(value string) (ingestionv1.ContentSelectionMode, bool) {
	switch value {
	case "observation":
		return ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_OBSERVATION, true
	case "enforcement":
		return ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_ENFORCEMENT, true
	default:
		return ingestionv1.ContentSelectionMode_CONTENT_SELECTION_MODE_UNSPECIFIED, false
	}
}

func contentSelectionFallback(value string) (ingestionv1.ContentSelectionFallbackReason, bool) {
	values := map[string]ingestionv1.ContentSelectionFallbackReason{
		"none":                ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_NONE,
		"observation":         ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_OBSERVATION,
		"unsupported_layout":  ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_UNSUPPORTED_LAYOUT,
		"processing_timeout":  ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_PROCESSING_TIMEOUT,
		"invalid_output":      ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_INVALID_OUTPUT,
		"ambiguous_mapping":   ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_AMBIGUOUS_MAPPING,
		"resource_limit":      ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_RESOURCE_LIMIT,
		"excessive_exclusion": ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_EXCESSIVE_EXCLUSION,
		"internal_error":      ingestionv1.ContentSelectionFallbackReason_CONTENT_SELECTION_FALLBACK_REASON_INTERNAL_ERROR,
	}
	result, ok := values[value]
	return result, ok
}

func contentSelectionReason(value string) (ingestionv1.ContentExclusionReason, bool) {
	values := map[string]ingestionv1.ContentExclusionReason{
		"title":                         ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_TITLE_PAGE,
		"copyright_imprint":             ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_COPYRIGHT_OR_IMPRINT,
		"dedication_ornamental":         ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_DEDICATION_OR_ORNAMENTAL_BLANK,
		"table_of_contents":             ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_TABLE_OR_LIST,
		"list_of_figures_tables":        ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_TABLE_OR_LIST,
		"table_or_list":                 ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_TABLE_OR_LIST,
		"index":                         ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_INDEX,
		"publisher_catalog_advertising": ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_PUBLISHER_CATALOG_OR_ADVERTISING,
		"also_by":                       ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_ALSO_BY,
		"colophon":                      ingestionv1.ContentExclusionReason_CONTENT_EXCLUSION_REASON_COLOPHON,
	}
	result, ok := values[value]
	return result, ok
}

func safeAuditValue(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char <= 0x20 || char > 0x7e {
			return false
		}
	}
	return true
}

func (w *Writer) flush(ctx context.Context) error {
	if len(w.pending) == 0 {
		return nil
	}
	uncompressed := make([]byte, 0, w.pendingSize)
	for _, chunk := range w.pending {
		uncompressed = protowire.AppendVarint(uncompressed, uint64(len(chunk.bytes)))
		uncompressed = append(uncompressed, chunk.bytes...)
	}
	compressed := w.encoder.EncodeAll(uncompressed, nil)
	sum := sha256.Sum256(compressed)
	index := len(w.descriptors)
	reference := path.Join(w.prefix, "shards", fmt.Sprintf("%06d.pb.zst", index))
	if err := w.store.Put(ctx, reference, compressed, sum); err != nil {
		return err
	}
	chunkCount := uint32(len(w.pending)) // #nosec G115 -- pending chunks are bounded to 256.
	w.descriptors = append(w.descriptors, &ingestionv1.ChunkShardDescriptorV1{
		Reference:            reference,
		Sha256:               append([]byte(nil), sum[:]...),
		CompressedByteSize:   int64(len(compressed)),
		UncompressedByteSize: int64(len(uncompressed)),
		ChunkCount:           chunkCount,
		FirstChunkOrder:      w.pending[0].message.Order,
		LastChunkOrder:       w.pending[len(w.pending)-1].message.Order,
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
	return errors.Join(result, w.encoder.Close())
}
