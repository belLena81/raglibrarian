package config

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/pkg/contracts"
	"github.com/belLena81/raglibrarian/pkg/process"
)

type LayoutConfig struct {
	RabbitURI, Queue, ResultExchange                                        string
	MinIOEndpoint, MinIOAccessKey, MinIOSecretKey, MinIOCAFile              string
	SourceBucket, PDFTextPath, EPUBParserPath                               string
	PolicyVersion, ParserVersion, ModelSHA256                               string
	MinIOInsecure                                                           bool
	MaximumSourceBytes, MaximumExtractedBytes, MaximumPageBytes             int64
	MaximumItemTextBytes                                                    int64
	MemoryLimitBytes, ParserSandboxMemoryBytes, ParserRuntimeHeadroomBytes  int64
	EPUBMaximumEntryBytes, EPUBMaximumExpandedBytes, EPUBMaximumTextBytes   int64
	MaximumPages                                                            uint32
	MinimumSignals, MaximumRanges, MaximumAttempts, WorkConcurrency         int
	MaximumItemsPerLocation, MaximumXMLTokens, MaximumXMLDepth              int
	EPUBMaximumEntries                                                      int
	EPUBMaximumSpineItems                                                   uint32
	MaximumExcludedRatio                                                    float64
	ParserTimeout, RabbitDialTimeout, RabbitHeartbeat, RabbitPublishTimeout time.Duration
	FirstRetryDelay, SecondRetryDelay, SubsequentRetryDelay                 time.Duration
	RunAs                                                                   process.Identity
}

func LoadLayout() (LayoutConfig, error) {
	rabbitURI, err := readSecret("LAYOUT_RABBITMQ_URI_FILE", 4096)
	if err != nil {
		return LayoutConfig{}, err
	}
	accessKey, err := readSecret("LAYOUT_MINIO_ACCESS_KEY_FILE", 1024)
	if err != nil {
		return LayoutConfig{}, err
	}
	secretKey, err := readSecret("LAYOUT_MINIO_SECRET_KEY_FILE", 1024)
	if err != nil {
		return LayoutConfig{}, err
	}
	endpoint, err := required("INGESTION_MINIO_ENDPOINT")
	if err != nil {
		return LayoutConfig{}, err
	}
	if err = validateEndpoint(endpoint); err != nil {
		return LayoutConfig{}, err
	}
	insecure, err := strictBool("INGESTION_MINIO_INSECURE", false)
	if err != nil {
		return LayoutConfig{}, err
	}
	maximumAttempts, err := boundedInt("INGESTION_MAX_ATTEMPTS", 4, 10)
	if err != nil {
		return LayoutConfig{}, err
	}
	workConcurrency, err := boundedInt("LAYOUT_WORK_CONCURRENCY", 1, 8)
	if err != nil {
		return LayoutConfig{}, err
	}
	memoryLimitBytes, err := boundedInt64("LAYOUT_MEMORY_LIMIT_BYTES", 2<<30, 64<<30)
	if err != nil {
		return LayoutConfig{}, err
	}
	parserSandboxMemoryBytes, err := boundedInt64("INGESTION_PARSER_SANDBOX_MEMORY_BYTES", DefaultParserSandboxMemoryBytes, MaximumParserSandboxMemoryBytes)
	if err != nil || parserSandboxMemoryBytes < DefaultParserSandboxMemoryBytes {
		return LayoutConfig{}, fmt.Errorf("invalid layout parser sandbox memory limit")
	}
	parserRuntimeHeadroomBytes, err := boundedInt64("INGESTION_PARSER_RUNTIME_HEADROOM_BYTES", 256<<20, 4<<30)
	if err != nil {
		return LayoutConfig{}, err
	}
	if int64(workConcurrency)*parserSandboxMemoryBytes+parserRuntimeHeadroomBytes > memoryLimitBytes {
		return LayoutConfig{}, fmt.Errorf("LAYOUT_WORK_CONCURRENCY exceeds LAYOUT_MEMORY_LIMIT_BYTES")
	}
	minimumSignals, err := fixedInt("INGESTION_CONTENT_SELECTION_MIN_SIGNALS", 2)
	if err != nil {
		return LayoutConfig{}, err
	}
	maximumRanges, err := fixedInt("INGESTION_CONTENT_SELECTION_MAX_RANGES", 256)
	if err != nil {
		return LayoutConfig{}, err
	}
	maximumSourceBytes, err := fixedInt64("INGESTION_MAX_SOURCE_BYTES", 25<<20)
	if err != nil {
		return LayoutConfig{}, err
	}
	maximumExtractedBytes, err := fixedInt64("INGESTION_MAX_EXTRACTED_BYTES", 128<<20)
	if err != nil {
		return LayoutConfig{}, err
	}
	maximumPages, err := fixedInt("INGESTION_MAX_PAGES", 1000)
	if err != nil {
		return LayoutConfig{}, err
	}
	maximumPageBytes, err := boundedInt64("INGESTION_MAX_PAGE_BYTES", 2<<20, 32<<20)
	if err != nil {
		return LayoutConfig{}, err
	}
	maximumItemsPerLocation, err := boundedInt("LAYOUT_MAX_ITEMS_PER_LOCATION", 2048, 4096)
	if err != nil {
		return LayoutConfig{}, err
	}
	maximumItemTextBytes, err := boundedInt64("LAYOUT_MAX_ITEM_TEXT_BYTES", 64<<10, 1<<20)
	if err != nil || maximumItemTextBytes > maximumPageBytes {
		return LayoutConfig{}, fmt.Errorf("invalid layout item text limit")
	}
	maximumXMLTokens, err := boundedInt("LAYOUT_MAX_XML_TOKENS", 2_000_000, 10_000_000)
	if err != nil {
		return LayoutConfig{}, err
	}
	maximumXMLDepth, err := boundedInt("LAYOUT_MAX_XML_DEPTH", 32, 128)
	if err != nil {
		return LayoutConfig{}, err
	}
	epubMaximumEntries, err := boundedInt("INGESTION_EPUB_MAX_ENTRIES", DefaultEPUBMaximumEntries, MaximumEPUBMaximumEntries)
	if err != nil {
		return LayoutConfig{}, err
	}
	epubMaximumSpineItems, err := boundedInt64("INGESTION_EPUB_MAX_SPINE_ITEMS", DefaultEPUBMaximumSpineItems, MaximumEPUBMaximumSpineItems)
	if err != nil {
		return LayoutConfig{}, err
	}
	epubMaximumEntryBytes, err := boundedInt64("INGESTION_EPUB_MAX_ENTRY_BYTES", DefaultEPUBMaximumEntryBytes, MaximumEPUBMaximumEntryBytes)
	if err != nil {
		return LayoutConfig{}, err
	}
	epubMaximumExpandedBytes, err := boundedInt64("INGESTION_EPUB_MAX_EXPANDED_BYTES", DefaultEPUBMaximumExpandedBytes, MaximumEPUBMaximumExpandedBytes)
	if err != nil {
		return LayoutConfig{}, err
	}
	epubMaximumTextBytes, err := boundedInt64("INGESTION_EPUB_MAX_TEXT_BYTES", DefaultEPUBMaximumTextBytes, MaximumEPUBMaximumTextBytes)
	if err != nil {
		return LayoutConfig{}, err
	}
	if epubMaximumExpandedBytes < epubMaximumEntryBytes || epubMaximumTextBytes > epubMaximumExpandedBytes {
		return LayoutConfig{}, fmt.Errorf("EPUB layout limits are inconsistent")
	}
	maximumExcludedRatio, err := strconv.ParseFloat(optional("INGESTION_CONTENT_SELECTION_MAX_EXCLUDED_RATIO", "0.25"), 64)
	if err != nil || math.IsNaN(maximumExcludedRatio) || math.IsInf(maximumExcludedRatio, 0) || maximumExcludedRatio != 0.25 {
		return LayoutConfig{}, fmt.Errorf("INGESTION_CONTENT_SELECTION_MAX_EXCLUDED_RATIO must be 0.25")
	}
	parserTimeout, err := boundedDuration("LAYOUT_PARSER_TIMEOUT", time.Minute, 10*time.Minute, 10*time.Minute)
	if err != nil {
		return LayoutConfig{}, err
	}
	dialTimeout, err := boundedDuration("INGESTION_RABBITMQ_DIAL_TIMEOUT", time.Second, time.Minute, 5*time.Second)
	if err != nil {
		return LayoutConfig{}, err
	}
	heartbeat, err := boundedDuration("INGESTION_RABBITMQ_HEARTBEAT", time.Second, time.Minute, 10*time.Second)
	if err != nil {
		return LayoutConfig{}, err
	}
	publishTimeout, err := boundedDuration("INGESTION_RABBITMQ_PUBLISH_TIMEOUT", time.Second, time.Minute, 10*time.Second)
	if err != nil {
		return LayoutConfig{}, err
	}
	firstRetry, err := boundedDuration("INGESTION_FIRST_RETRY_DELAY", time.Second, 10*time.Minute, 5*time.Second)
	if err != nil {
		return LayoutConfig{}, err
	}
	secondRetry, err := boundedDuration("INGESTION_SECOND_RETRY_DELAY", time.Second, 10*time.Minute, 30*time.Second)
	if err != nil {
		return LayoutConfig{}, err
	}
	subsequentRetry, err := boundedDuration("INGESTION_SUBSEQUENT_RETRY_DELAY", time.Second, 10*time.Minute, 2*time.Minute)
	if err != nil {
		return LayoutConfig{}, err
	}
	uid, err := boundedInt("RUN_AS_UID", 65532, 1<<30)
	if err != nil {
		return LayoutConfig{}, err
	}
	gid, err := boundedInt("RUN_AS_GID", 65532, 1<<30)
	if err != nil {
		return LayoutConfig{}, err
	}
	modelSHA256 := optional("INGESTION_CONTENT_SELECTION_MODEL_SHA256", DefaultContentSelectionModelSHA)
	modelDigest, digestErr := hex.DecodeString(modelSHA256)
	if digestErr != nil || len(modelDigest) != 32 {
		return LayoutConfig{}, fmt.Errorf("INGESTION_CONTENT_SELECTION_MODEL_SHA256 must be a SHA-256 digest")
	}
	return LayoutConfig{
		RabbitURI: rabbitURI, Queue: optional("LAYOUT_REQUEST_QUEUE", contracts.QueueIngestionContentSelectionRequests), ResultExchange: optional("LAYOUT_RESULT_EXCHANGE", contracts.ExchangeIngestionContentSelectionResults),
		MinIOEndpoint: endpoint, MinIOAccessKey: accessKey, MinIOSecretKey: secretKey, MinIOCAFile: strings.TrimSpace(optional("INGESTION_MINIO_CA_FILE", "")),
		SourceBucket: optional("INGESTION_SOURCE_BUCKET", "original-books"), PDFTextPath: optional("INGESTION_PDFTOTEXT_PATH", "/usr/bin/pdftotext"), EPUBParserPath: optional("INGESTION_EPUB_PARSER_PATH", "/usr/local/bin/epub-parser"),
		PolicyVersion: optional("INGESTION_CONTENT_SELECTION_POLICY_VERSION", DefaultContentSelectionPolicy), ParserVersion: optional("INGESTION_CONTENT_SELECTION_PARSER_VERSION", DefaultContentSelectionParser), ModelSHA256: modelSHA256,
		MinIOInsecure: insecure, MaximumSourceBytes: maximumSourceBytes, MaximumExtractedBytes: maximumExtractedBytes, MaximumPageBytes: maximumPageBytes, MaximumItemTextBytes: maximumItemTextBytes, MemoryLimitBytes: memoryLimitBytes, ParserSandboxMemoryBytes: parserSandboxMemoryBytes, ParserRuntimeHeadroomBytes: parserRuntimeHeadroomBytes, MaximumPages: uint32(maximumPages), MinimumSignals: minimumSignals, MaximumRanges: maximumRanges, MaximumExcludedRatio: maximumExcludedRatio, MaximumAttempts: maximumAttempts, WorkConcurrency: workConcurrency, MaximumItemsPerLocation: maximumItemsPerLocation, MaximumXMLTokens: maximumXMLTokens, MaximumXMLDepth: maximumXMLDepth, // #nosec G115 -- fixed supported maximum is positive.
		EPUBMaximumEntries: epubMaximumEntries, EPUBMaximumSpineItems: uint32(epubMaximumSpineItems), EPUBMaximumEntryBytes: epubMaximumEntryBytes, EPUBMaximumExpandedBytes: epubMaximumExpandedBytes, EPUBMaximumTextBytes: epubMaximumTextBytes, // #nosec G115 -- bounded to uint32 maximum.
		ParserTimeout: parserTimeout, RabbitDialTimeout: dialTimeout, RabbitHeartbeat: heartbeat, RabbitPublishTimeout: publishTimeout,
		FirstRetryDelay: firstRetry, SecondRetryDelay: secondRetry, SubsequentRetryDelay: subsequentRetry, RunAs: process.Identity{UID: uid, GID: gid},
	}, nil
}
