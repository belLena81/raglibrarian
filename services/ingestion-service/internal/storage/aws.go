package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/artifact"
)

type s3API interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	ListObjectVersions(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
}

func (s *AWSSourceStore) Ready(ctx context.Context) bool {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &s.bucket})
	return err == nil
}

func NewAWSS3Client(ctx context.Context, region string) (s3API, error) {
	if strings.TrimSpace(region) == "" {
		return nil, errors.New("AWS region is required")
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, errors.New("AWS storage configuration unavailable")
	}
	return s3.NewFromConfig(cfg), nil
}

type AWSSourceStore struct {
	client s3API
	bucket string
}

func NewAWSSourceStore(client s3API, bucket string) (*AWSSourceStore, error) {
	if client == nil || !validBucket(bucket) {
		return nil, errors.New("invalid AWS source store")
	}
	return &AWSSourceStore{client: client, bucket: bucket}, nil
}

func (s *AWSSourceStore) Open(ctx context.Context, reference string) (io.ReadCloser, int64, error) {
	if !validSourceReference(reference) {
		return nil, 0, errors.New("invalid source reference")
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &reference})
	if err != nil || result.Body == nil || result.ContentLength == nil || *result.ContentLength < 1 {
		if result != nil && result.Body != nil {
			_ = result.Body.Close()
		}
		return nil, 0, errors.New("source unavailable")
	}
	return result.Body, *result.ContentLength, nil
}

type AWSArtifactStore struct {
	client    s3API
	bucket    string
	kmsKeyARN string
}

const maximumVersionCleanupPasses = 256

func (s *AWSArtifactStore) Ready(ctx context.Context) bool {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &s.bucket})
	return err == nil
}

func NewAWSArtifactStore(client s3API, bucket, kmsKeyARN string) (*AWSArtifactStore, error) {
	if client == nil || !validBucket(bucket) || strings.TrimSpace(kmsKeyARN) == "" {
		return nil, errors.New("invalid AWS artifact store")
	}
	return &AWSArtifactStore{client: client, bucket: bucket, kmsKeyARN: kmsKeyARN}, nil
}

func (s *AWSArtifactStore) Put(ctx context.Context, reference string, contents []byte, checksum [sha256.Size]byte) error {
	if !validArtifactReference(reference) || len(contents) == 0 {
		return errors.New("invalid artifact")
	}
	expected := hex.EncodeToString(checksum[:])
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucket, Key: &reference, Body: bytes.NewReader(contents), ContentLength: int64Ptr(int64(len(contents))),
		ContentType: stringPtr(contentType(reference)), Metadata: map[string]string{"sha256": expected},
		ServerSideEncryption: types.ServerSideEncryptionAwsKms, SSEKMSKeyId: &s.kmsKeyARN, IfNoneMatch: stringPtr("*"),
	})
	conflict := isPreconditionFailed(err)
	if err != nil && !conflict {
		return errors.New("artifact storage unavailable")
	}
	receipt, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &reference})
	if err != nil {
		return errors.New("artifact receipt mismatch")
	}
	if receipt.ContentLength == nil || *receipt.ContentLength != int64(len(contents)) || metadataValue(receipt.Metadata, "sha256") != expected {
		if conflict {
			return artifact.ErrArtifactConflict
		}
		return errors.New("artifact receipt mismatch")
	}
	return nil
}

func isPreconditionFailed(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && (apiErr.ErrorCode() == "PreconditionFailed" || apiErr.ErrorCode() == "ConditionalRequestConflict")
}

func (s *AWSArtifactStore) Delete(ctx context.Context, reference string) error {
	if !validArtifactReference(reference) {
		return errors.New("invalid artifact reference")
	}
	return s.deleteVersions(ctx, reference, true)
}

func (s *AWSArtifactStore) DeletePrefix(ctx context.Context, prefix string) error {
	if !validArtifactPrefix(prefix) {
		return errors.New("invalid artifact prefix")
	}
	return s.deleteVersions(ctx, prefix, false)
}

func (s *AWSArtifactStore) deleteVersions(ctx context.Context, target string, exact bool) error {
	for range maximumVersionCleanupPasses {
		listed, err := s.client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
			Bucket: &s.bucket, Prefix: &target,
		})
		if err != nil {
			return errors.New("artifact cleanup unavailable")
		}
		versions := make([]objectVersion, 0, len(listed.Versions)+len(listed.DeleteMarkers))
		for _, version := range listed.Versions {
			validated, validateErr := validateObjectVersion(target, exact, version.Key, version.VersionId)
			if validateErr != nil {
				return validateErr
			}
			versions = append(versions, validated)
		}
		for _, marker := range listed.DeleteMarkers {
			validated, validateErr := validateObjectVersion(target, exact, marker.Key, marker.VersionId)
			if validateErr != nil {
				return validateErr
			}
			versions = append(versions, validated)
		}
		if len(versions) == 0 {
			if listed.IsTruncated != nil && *listed.IsTruncated {
				return errors.New("artifact cleanup pagination invalid")
			}
			return nil
		}
		for _, version := range versions {
			if err = s.deleteVersion(ctx, version); err != nil {
				return err
			}
		}
	}
	return errors.New("artifact cleanup limit exceeded")
}

type objectVersion struct {
	key       string
	versionID string
}

func validateObjectVersion(target string, exact bool, key, versionID *string) (objectVersion, error) {
	if key == nil || !validArtifactReference(*key) || versionID == nil || *versionID == "" {
		return objectVersion{}, errors.New("artifact cleanup boundary violated")
	}
	if (exact && *key != target) || (!exact && !strings.HasPrefix(*key, target)) {
		return objectVersion{}, errors.New("artifact cleanup boundary violated")
	}
	return objectVersion{key: *key, versionID: *versionID}, nil
}

func (s *AWSArtifactStore) deleteVersion(ctx context.Context, version objectVersion) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &version.key, VersionId: &version.versionID})
	if err != nil {
		return errors.New("artifact cleanup unavailable")
	}
	return nil
}

func validBucket(value string) bool {
	if len(value) < 3 || len(value) > 63 || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' && char != '.' {
			return false
		}
	}
	return true
}

func validSourceReference(reference string) bool {
	if !strings.HasPrefix(reference, "originals/") || strings.Count(reference, "/") != 1 || !strings.HasSuffix(reference, ".pdf") || len(reference) > 512 {
		return false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(reference, "originals/"), ".pdf")
	if name == "" {
		return false
	}
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func int64Ptr(value int64) *int64    { return &value }
func stringPtr(value string) *string { return &value }
