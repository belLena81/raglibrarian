// Package domain owns Ingestion's processing invariants and value objects.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidJob    = errors.New("invalid processing job")
	ErrLeaseNotOwned = errors.New("processing lease not owned")
	ErrTerminalJob   = errors.New("processing job is terminal")
)

type JobState string

const (
	JobQueued     JobState = "queued"
	JobProcessing JobState = "processing"
	JobRetrying   JobState = "retrying"
	JobCompleted  JobState = "completed"
	JobFailed     JobState = "failed"
)

type FailureCategory string

const (
	FailureEncryptedDocument       FailureCategory = "encrypted_document"
	FailureExtractionNotPermitted  FailureCategory = "extraction_not_permitted"
	FailureMalformedDocument       FailureCategory = "malformed_document"
	FailureUnsupportedDocument     FailureCategory = "unsupported_document"
	FailureNoExtractableText       FailureCategory = "no_extractable_text"
	FailureResourceLimitExceeded   FailureCategory = "resource_limit_exceeded"
	FailureSourceIntegrityMismatch FailureCategory = "source_integrity_mismatch"
	FailureProcessingTimeout       FailureCategory = "processing_timeout"
	FailureDependencyUnavailable   FailureCategory = "dependency_unavailable"
	FailureInternalProcessing      FailureCategory = "internal_processing_error"
)

func (c FailureCategory) Valid() bool {
	switch c {
	case FailureEncryptedDocument, FailureExtractionNotPermitted, FailureMalformedDocument, FailureUnsupportedDocument,
		FailureNoExtractableText, FailureResourceLimitExceeded, FailureSourceIntegrityMismatch,
		FailureProcessingTimeout, FailureDependencyUnavailable, FailureInternalProcessing:
		return true
	default:
		return false
	}
}

type ProcessingJob struct {
	id             string
	bookID         string
	sourceSHA256   [32]byte
	configDigest   string
	state          JobState
	attempts       int
	leaseOwner     string
	leaseExpiresAt time.Time
	nextAttemptAt  time.Time
	failure        FailureCategory
	manifestRef    string
	manifestSHA256 [32]byte
	manifestSize   int64
	createdAt      time.Time
	updatedAt      time.Time
}

func NewProcessingJob(id, bookID string, sourceSHA256 [32]byte, configDigest string, now time.Time) (ProcessingJob, error) {
	if !validIdentifier(id) || !validIdentifier(bookID) || strings.TrimSpace(configDigest) == "" || len(configDigest) > 128 || now.IsZero() {
		return ProcessingJob{}, ErrInvalidJob
	}
	return ProcessingJob{id: id, bookID: bookID, sourceSHA256: sourceSHA256, configDigest: configDigest, state: JobQueued, createdAt: now.UTC(), updatedAt: now.UTC()}, nil
}

func RestoreProcessingJob(id, bookID string, sourceSHA256 [32]byte, configDigest string, state JobState, attempts int, leaseOwner string, leaseExpiresAt, nextAttemptAt time.Time, failure FailureCategory, manifestRef string, manifestSHA256 [32]byte, manifestSize int64, createdAt, updatedAt time.Time) (ProcessingJob, error) {
	job := ProcessingJob{id: id, bookID: bookID, sourceSHA256: sourceSHA256, configDigest: configDigest, state: state, attempts: attempts, leaseOwner: leaseOwner, leaseExpiresAt: leaseExpiresAt, nextAttemptAt: nextAttemptAt, failure: failure, manifestRef: manifestRef, manifestSHA256: manifestSHA256, manifestSize: manifestSize, createdAt: createdAt, updatedAt: updatedAt}
	if !validIdentifier(id) || !validIdentifier(bookID) || attempts < 0 || createdAt.IsZero() || updatedAt.IsZero() {
		return ProcessingJob{}, ErrInvalidJob
	}
	return job, nil
}

func (j *ProcessingJob) Claim(owner string, now time.Time, lease time.Duration) error {
	if j.terminal() {
		return ErrTerminalJob
	}
	if !validIdentifier(owner) || now.IsZero() || lease <= 0 {
		return ErrInvalidJob
	}
	if j.state == JobProcessing && now.Before(j.leaseExpiresAt) && owner != j.leaseOwner {
		return ErrLeaseNotOwned
	}
	j.state = JobProcessing
	j.attempts++
	j.leaseOwner = owner
	j.leaseExpiresAt = now.Add(lease).UTC()
	j.updatedAt = now.UTC()
	return nil
}

func (j *ProcessingJob) RenewLease(owner string, now time.Time, lease time.Duration) error {
	if j.state != JobProcessing || j.leaseOwner != owner || now.After(j.leaseExpiresAt) {
		return ErrLeaseNotOwned
	}
	j.leaseExpiresAt = now.Add(lease).UTC()
	j.updatedAt = now.UTC()
	return nil
}

func (j *ProcessingJob) ScheduleRetry(owner string, nextAttemptAt, now time.Time) error {
	if err := j.requireOwner(owner, now); err != nil {
		return err
	}
	if !nextAttemptAt.After(now) {
		return ErrInvalidJob
	}
	j.state = JobRetrying
	j.nextAttemptAt = nextAttemptAt.UTC()
	j.clearLease()
	j.updatedAt = now.UTC()
	return nil
}

func (j *ProcessingJob) Complete(owner, manifestRef string, manifestSHA256 [32]byte, manifestSize int64, now time.Time) error {
	if err := j.requireOwner(owner, now); err != nil {
		return err
	}
	if strings.TrimSpace(manifestRef) == "" || len(manifestRef) > 1024 || manifestSize < 1 {
		return ErrInvalidJob
	}
	j.state = JobCompleted
	j.manifestRef = manifestRef
	j.manifestSHA256 = manifestSHA256
	j.manifestSize = manifestSize
	j.clearLease()
	j.updatedAt = now.UTC()
	return nil
}

func (j *ProcessingJob) Fail(owner string, category FailureCategory, now time.Time) error {
	if err := j.requireOwner(owner, now); err != nil {
		return err
	}
	if !category.Valid() {
		return ErrInvalidJob
	}
	j.state = JobFailed
	j.failure = category
	j.clearLease()
	j.updatedAt = now.UTC()
	return nil
}

func (j ProcessingJob) ID() string                { return j.id }
func (j ProcessingJob) BookID() string            { return j.bookID }
func (j ProcessingJob) SourceSHA256() [32]byte    { return j.sourceSHA256 }
func (j ProcessingJob) ConfigDigest() string      { return j.configDigest }
func (j ProcessingJob) State() JobState           { return j.state }
func (j ProcessingJob) Attempts() int             { return j.attempts }
func (j ProcessingJob) LeaseOwner() string        { return j.leaseOwner }
func (j ProcessingJob) LeaseExpiresAt() time.Time { return j.leaseExpiresAt }
func (j ProcessingJob) NextAttemptAt() time.Time  { return j.nextAttemptAt }
func (j ProcessingJob) Failure() FailureCategory  { return j.failure }
func (j ProcessingJob) ManifestReference() string { return j.manifestRef }
func (j ProcessingJob) ManifestSHA256() [32]byte  { return j.manifestSHA256 }
func (j ProcessingJob) ManifestByteSize() int64   { return j.manifestSize }
func (j ProcessingJob) CreatedAt() time.Time      { return j.createdAt }
func (j ProcessingJob) UpdatedAt() time.Time      { return j.updatedAt }

func (j ProcessingJob) terminal() bool { return j.state == JobCompleted || j.state == JobFailed }

func (j ProcessingJob) requireOwner(owner string, now time.Time) error {
	if j.terminal() {
		return ErrTerminalJob
	}
	if j.state != JobProcessing || owner != j.leaseOwner || now.After(j.leaseExpiresAt) {
		return ErrLeaseNotOwned
	}
	return nil
}

func (j *ProcessingJob) clearLease() {
	j.leaseOwner = ""
	j.leaseExpiresAt = time.Time{}
}

func validIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char <= 0x20 || char == '/' || char == '\\' {
			return false
		}
	}
	return true
}

func (j ProcessingJob) String() string {
	return fmt.Sprintf("ProcessingJob{id:%q,state:%q,attempts:%d}", j.id, j.state, j.attempts)
}
