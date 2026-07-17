package repository

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestMinIOObjectStoreListCompletedHonorsLimit(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Has("location") {
			_, _ = response.Write([]byte("<LocationConstraint xmlns=\"http://s3.amazonaws.com/doc/2006-03-01/\">us-east-1</LocationConstraint>"))
			return
		}
		requests++
		if request.URL.Query().Get("max-keys") != "2" || request.URL.Query().Get("start-after") != "originals/cursor.pdf" {
			t.Errorf("listing query = %q", request.URL.RawQuery)
		}
		writeListResponse(response, []string{"originals/one.pdf", "originals/two.pdf", "originals/three.pdf"}, true)
	}))
	defer server.Close()

	store := testMinIOStore(t, server)
	objects, cursor, err := store.ListCompleted(context.Background(), "originals/", "originals/cursor.pdf", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 || cursor != "originals/two.pdf" {
		t.Fatalf("objects = %#v, cursor = %q", objects, cursor)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want one page", requests)
	}
}

func TestMinIOObjectStoreListCompletedReturnsEmptyCursorBelowLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Has("location") {
			_, _ = response.Write([]byte("<LocationConstraint xmlns=\"http://s3.amazonaws.com/doc/2006-03-01/\">us-east-1</LocationConstraint>"))
			return
		}
		writeListResponse(response, []string{"originals/one.pdf"}, false)
	}))
	defer server.Close()

	objects, cursor, err := testMinIOStore(t, server).ListCompleted(context.Background(), "originals/", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || cursor != "" {
		t.Fatalf("objects = %#v, cursor = %q", objects, cursor)
	}
}

func TestMinIOObjectStoreListCompletedReturnsCancellationAndStorageErrors(t *testing.T) {
	t.Run("parent cancellation", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := testMinIOStore(t, server).ListCompleted(ctx, "originals/", "", 1)
		if err == nil {
			t.Fatal("cancelled listing unexpectedly succeeded")
		}
	})

	t.Run("storage error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Query().Has("location") {
				_, _ = response.Write([]byte("<LocationConstraint xmlns=\"http://s3.amazonaws.com/doc/2006-03-01/\">us-east-1</LocationConstraint>"))
				return
			}
			http.Error(response, "storage unavailable", http.StatusInternalServerError)
		}))
		defer server.Close()
		_, _, err := testMinIOStore(t, server).ListCompleted(context.Background(), "originals/", "", 1)
		if err == nil {
			t.Fatal("storage failure unexpectedly succeeded")
		}
	})
}

func testMinIOStore(t *testing.T, server *httptest.Server) *MinIOObjectStore {
	t.Helper()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := minio.New(endpoint.Host, &minio.Options{
		Creds:  credentials.NewStaticV4("access", "secret", ""),
		Secure: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewMinIOObjectStore(client, "original-books")
}

func writeListResponse(response http.ResponseWriter, keys []string, truncated bool) {
	response.Header().Set("Content-Type", "application/xml")
	continuation := ""
	if truncated {
		continuation = "<NextContinuationToken>next-page</NextContinuationToken>"
	}
	_, _ = response.Write([]byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?><ListBucketResult xmlns=\"http://s3.amazonaws.com/doc/2006-03-01/\"><Name>original-books</Name><KeyCount>" + strconv.Itoa(len(keys)) + "</KeyCount><MaxKeys>100</MaxKeys><IsTruncated>" + strconv.FormatBool(truncated) + "</IsTruncated>" + continuation))
	for _, key := range keys {
		_, _ = response.Write([]byte("<Contents><Key>" + key + "</Key><LastModified>2026-07-17T00:00:00.000Z</LastModified><ETag>\"etag\"</ETag><Size>1</Size><StorageClass>STANDARD</StorageClass></Contents>"))
	}
	_, _ = response.Write([]byte("</ListBucketResult>"))
}
