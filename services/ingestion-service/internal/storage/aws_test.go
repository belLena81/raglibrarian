package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/artifact"
)

type fakeS3 struct {
	putInput    *s3.PutObjectInput
	putErr      error
	headOutput  *s3.HeadObjectOutput
	listOutputs []*s3.ListObjectVersionsOutput
	listErr     error
	listInputs  []*s3.ListObjectVersionsInput
	deleteErr   error
	deletes     []*s3.DeleteObjectInput
}

func (f *fakeS3) GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeS3) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return f.headOutput, nil
}

func (f *fakeS3) HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, nil
}

func (f *fakeS3) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.putInput = input
	return &s3.PutObjectOutput{}, f.putErr
}

func (f *fakeS3) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.deletes = append(f.deletes, input)
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &s3.DeleteObjectOutput{}, nil
}

func (f *fakeS3) ListObjectVersions(_ context.Context, input *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	f.listInputs = append(f.listInputs, input)
	if f.listErr != nil {
		return nil, f.listErr
	}
	index := len(f.listInputs) - 1
	if index >= len(f.listOutputs) {
		return &s3.ListObjectVersionsOutput{}, nil
	}
	return f.listOutputs[index], nil
}

func TestAWSArtifactPutIsConditionalAndKMSProtected(t *testing.T) {
	contents := []byte("artifact")
	checksum := sha256.Sum256(contents)
	expectedChecksum := hex.EncodeToString(checksum[:])
	fake := &fakeS3{headOutput: &s3.HeadObjectOutput{
		ContentLength: int64Ptr(int64(len(contents))),
		Metadata:      map[string]string{"sha256": expectedChecksum},
	}}
	store, err := NewAWSArtifactStore(fake, "artifact-bucket", "arn:aws:kms:region:account:key/id")
	if err != nil {
		t.Fatal(err)
	}
	reference := validTestPrefix() + "manifest.pb"
	if err = store.Put(context.Background(), reference, contents, checksum); err != nil {
		t.Fatal(err)
	}
	if fake.putInput == nil || fake.putInput.IfNoneMatch == nil || *fake.putInput.IfNoneMatch != "*" {
		t.Fatal("artifact write must use an immutable conditional request")
	}
	if fake.putInput.ServerSideEncryption != types.ServerSideEncryptionAwsKms || fake.putInput.SSEKMSKeyId == nil || *fake.putInput.SSEKMSKeyId != "arn:aws:kms:region:account:key/id" {
		t.Fatal("artifact write must require the configured KMS key")
	}
	body, err := io.ReadAll(fake.putInput.Body)
	if err != nil || string(body) != string(contents) {
		t.Fatalf("unexpected uploaded body: %q %v", body, err)
	}
}

func TestAWSArtifactPutVerifiesConditionalConflictReceipt(t *testing.T) {
	contents := []byte("artifact")
	checksum := sha256.Sum256(contents)
	fake := &fakeS3{
		putErr: &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "exists"},
		headOutput: &s3.HeadObjectOutput{
			ContentLength: int64Ptr(int64(len(contents))),
			Metadata:      map[string]string{"sha256": "different"},
		},
	}
	store, _ := NewAWSArtifactStore(fake, "artifact-bucket", "kms-key")
	err := store.Put(context.Background(), validTestPrefix()+"manifest.pb", contents, checksum)
	if !errors.Is(err, artifact.ErrArtifactConflict) {
		t.Fatalf("expected verified conflict, got %v", err)
	}
}

func TestAWSDeletePrefixRemovesVersionsAndDeleteMarkersAcrossPages(t *testing.T) {
	prefix := validTestPrefix()
	manifest := prefix + "manifest.pb"
	shard := prefix + "shards/000000.pb.zst"
	fake := &fakeS3{listOutputs: []*s3.ListObjectVersionsOutput{
		{
			Versions:            []types.ObjectVersion{{Key: &manifest, VersionId: stringPtr("version-1")}},
			IsTruncated:         boolPtr(true),
			NextKeyMarker:       &manifest,
			NextVersionIdMarker: stringPtr("version-1"),
		},
		{
			DeleteMarkers: []types.DeleteMarkerEntry{{Key: &shard, VersionId: stringPtr("marker-1")}},
			IsTruncated:   boolPtr(false),
		},
	}}
	store, _ := NewAWSArtifactStore(fake, "artifact-bucket", "kms-key")
	if err := store.DeletePrefix(context.Background(), prefix); err != nil {
		t.Fatal(err)
	}
	if len(fake.deletes) != 2 {
		t.Fatalf("expected every version and marker to be deleted, got %d", len(fake.deletes))
	}
	if fake.deletes[0].VersionId == nil || *fake.deletes[0].VersionId != "version-1" || fake.deletes[1].VersionId == nil || *fake.deletes[1].VersionId != "marker-1" {
		t.Fatal("cleanup must send explicit version IDs")
	}
	if len(fake.listInputs) != 3 || fake.listInputs[1].KeyMarker != nil || fake.listInputs[2].KeyMarker != nil {
		t.Fatal("cleanup must restart each version listing and verify the prefix is empty")
	}
}

func TestAWSDeleteRemovesAllVersionsOfOneArtifact(t *testing.T) {
	reference := validTestPrefix() + "manifest.pb"
	fake := &fakeS3{listOutputs: []*s3.ListObjectVersionsOutput{{
		Versions:      []types.ObjectVersion{{Key: &reference, VersionId: stringPtr("version-1")}},
		DeleteMarkers: []types.DeleteMarkerEntry{{Key: &reference, VersionId: stringPtr("marker-1")}},
		IsTruncated:   boolPtr(false),
	}}}
	store, _ := NewAWSArtifactStore(fake, "artifact-bucket", "kms-key")
	if err := store.Delete(context.Background(), reference); err != nil {
		t.Fatal(err)
	}
	if len(fake.deletes) != 2 || fake.deletes[0].VersionId == nil || fake.deletes[1].VersionId == nil {
		t.Fatal("single-artifact rollback must remove versions and delete markers")
	}
}

func TestAWSDeletePrefixFailsClosedWithoutCompletingPartialCleanup(t *testing.T) {
	prefix := validTestPrefix()
	outside := validTestPrefixForBook("other-book") + "manifest.pb"
	fake := &fakeS3{listOutputs: []*s3.ListObjectVersionsOutput{{
		Versions:    []types.ObjectVersion{{Key: &outside, VersionId: stringPtr("version-1")}},
		IsTruncated: boolPtr(false),
	}}}
	store, _ := NewAWSArtifactStore(fake, "artifact-bucket", "kms-key")
	if err := store.DeletePrefix(context.Background(), prefix); err == nil {
		t.Fatal("cleanup must reject an object outside the claimed prefix")
	}
	if len(fake.deletes) != 0 {
		t.Fatal("cleanup must not delete a boundary-violating object")
	}

	fake = &fakeS3{listOutputs: []*s3.ListObjectVersionsOutput{{IsTruncated: boolPtr(true)}}}
	store, _ = NewAWSArtifactStore(fake, "artifact-bucket", "kms-key")
	if err := store.DeletePrefix(context.Background(), prefix); err == nil {
		t.Fatal("cleanup must reject incomplete version pagination")
	}
}

func TestAWSDeletePrefixPropagatesListAndDeleteFailures(t *testing.T) {
	prefix := validTestPrefix()
	fake := &fakeS3{listErr: errors.New("list unavailable")}
	store, _ := NewAWSArtifactStore(fake, "artifact-bucket", "kms-key")
	if err := store.DeletePrefix(context.Background(), prefix); err == nil {
		t.Fatal("cleanup must retry after a version listing failure")
	}

	manifest := prefix + "manifest.pb"
	fake = &fakeS3{
		listOutputs: []*s3.ListObjectVersionsOutput{{
			Versions:    []types.ObjectVersion{{Key: &manifest, VersionId: stringPtr("version-1")}},
			IsTruncated: boolPtr(false),
		}},
		deleteErr: errors.New("delete unavailable"),
	}
	store, _ = NewAWSArtifactStore(fake, "artifact-bucket", "kms-key")
	if err := store.DeletePrefix(context.Background(), prefix); err == nil {
		t.Fatal("cleanup must retry after an explicit version deletion failure")
	}
}

func TestArtifactReferencesHaveExactImmutableShape(t *testing.T) {
	prefix := validTestPrefix()
	valid := []string{prefix + "manifest.pb", prefix + "shards/000000.pb.zst", prefix + "shards/999999.pb.zst"}
	for _, reference := range valid {
		if !validArtifactReference(reference) {
			t.Fatalf("expected valid artifact reference: %q", reference)
		}
	}
	invalid := []string{
		"books/book/source/config/manifest.pb",
		validTestPrefixForBook("../other") + "manifest.pb",
		prefix + "other.pb",
		prefix + "shards/1.pb.zst",
		prefix + "shards/000000.pb.zst/extra",
		prefix + "../manifest.pb",
		"books/book/" + repeated("A", 64) + "/" + repeated("b", 64) + "/manifest.pb",
	}
	for _, reference := range invalid {
		if validArtifactReference(reference) {
			t.Fatalf("expected invalid artifact reference: %q", reference)
		}
	}
	if !validArtifactPrefix(prefix) || validArtifactPrefix(prefix+"shards/") || validArtifactPrefix("books/book/source/config/") {
		t.Fatal("artifact cleanup prefix must match one exact immutable artifact set")
	}
}

func validTestPrefix() string {
	return validTestPrefixForBook("book-1")
}

func validTestPrefixForBook(bookID string) string {
	return "books/" + bookID + "/" + repeated("a", 64) + "/" + repeated("b", 64) + "/"
}

func repeated(value string, count int) string {
	result := ""
	for range count {
		result += value
	}
	return result
}

func boolPtr(value bool) *bool { return &value }
