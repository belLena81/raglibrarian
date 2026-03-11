package proto_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/belLena81/raglibrarian/pkg/proto/metadatapb"
	retrievalpb "github.com/belLena81/raglibrarian/pkg/proto/retrivalpb"
)

// ── retrievalpb ───────────────────────────────────────────────────────────────

// Compile-time: UnimplementedRetrievalServiceServer satisfies RetrievalServiceServer.
// If a new RPC is added to the interface without updating the Unimplemented struct,
// this line fails to compile — exactly the forward-compatibility guarantee we want.
var _ retrievalpb.RetrievalServiceServer = (*retrievalpb.UnimplementedRetrievalServiceServer)(nil)

func TestSearchRequest_Accessors(t *testing.T) {
	req := &retrievalpb.SearchRequest{
		QueryId:  "q-1",
		Question: "What is a goroutine?",
		TopK:     5,
	}
	assert.Equal(t, "q-1", req.GetQueryId())
	assert.Equal(t, "What is a goroutine?", req.GetQuestion())
	assert.Equal(t, int32(5), req.GetTopK())
}

func TestSearchResultItem_Accessors(t *testing.T) {
	item := &retrievalpb.SearchResultItem{
		BookId:  "b-1",
		Title:   "The Go Programming Language",
		Author:  "Donovan & Kernighan",
		Year:    2015,
		Chapter: "Chapter 9 — Concurrency",
		Pages:   []int32{217, 218, 219},
		Passage: "Goroutines are multiplexed onto a small number of OS threads.",
		Score:   0.94,
	}
	assert.Equal(t, "b-1", item.GetBookId())
	assert.Equal(t, "The Go Programming Language", item.GetTitle())
	assert.Equal(t, "Donovan & Kernighan", item.GetAuthor())
	assert.Equal(t, int32(2015), item.GetYear())
	assert.Equal(t, "Chapter 9 — Concurrency", item.GetChapter())
	assert.Equal(t, []int32{217, 218, 219}, item.GetPages())
	assert.Equal(t, float32(0.94), item.GetScore())
}

func TestSearchResponse_NilResults_ReturnsNil(t *testing.T) {
	// Callers must guard against nil Results — proto3 repeated fields default to nil.
	resp := &retrievalpb.SearchResponse{}
	assert.Nil(t, resp.GetResults())
}

func TestSearchResponse_WithResults(t *testing.T) {
	resp := &retrievalpb.SearchResponse{
		Results: []*retrievalpb.SearchResultItem{
			{BookId: "b-1", Score: 0.9},
			{BookId: "b-2", Score: 0.7},
		},
	}
	assert.Len(t, resp.GetResults(), 2)
	assert.Equal(t, "b-1", resp.GetResults()[0].GetBookId())
}

func TestRetrievalServiceDesc_MethodNames(t *testing.T) {
	// Verify the service descriptor is consistent with the proto file.
	desc := retrievalpb.RetrievalService_ServiceDesc
	assert.Equal(t, "raglibrarian.retrieval.v1.RetrievalService", desc.ServiceName)
	assert.Len(t, desc.Methods, 1)
	assert.Equal(t, "Search", desc.Methods[0].MethodName)
	assert.Empty(t, desc.Streams, "RetrievalService has no streaming RPCs")
}

func TestRetrievalService_FullMethodName(t *testing.T) {
	assert.Equal(t,
		"/raglibrarian.retrieval.v1.RetrievalService/Search",
		retrievalpb.RetrievalService_Search_FullMethodName,
	)
}

// ── metadatapb ────────────────────────────────────────────────────────────────

// Compile-time: UnimplementedMetadataServiceServer satisfies MetadataServiceServer.
var _ metadatapb.MetadataServiceServer = (*metadatapb.UnimplementedMetadataServiceServer)(nil)

func TestUpdateBookStatusRequest_Accessors(t *testing.T) {
	req := &metadatapb.UpdateBookStatusRequest{
		BookId: "book-uuid",
		Status: "indexing",
	}
	assert.Equal(t, "book-uuid", req.GetBookId())
	assert.Equal(t, "indexing", req.GetStatus())
}

func TestUpdateBookStatusResponse_Accessors(t *testing.T) {
	resp := &metadatapb.UpdateBookStatusResponse{
		BookId: "book-uuid",
		Status: "indexed",
	}
	assert.Equal(t, "book-uuid", resp.GetBookId())
	assert.Equal(t, "indexed", resp.GetStatus())
}

func TestBookProto_Accessors(t *testing.T) {
	book := &metadatapb.BookProto{
		BookId:    "b-1",
		Title:     "Clean Architecture",
		Author:    "Robert Martin",
		Year:      2017,
		Status:    "indexed",
		Tags:      []string{"architecture", "ddd"},
		S3Key:     "books/b-1/file.pdf",
		CreatedAt: 1700000000,
		UpdatedAt: 1700000001,
	}
	assert.Equal(t, "b-1", book.GetBookId())
	assert.Equal(t, "Clean Architecture", book.GetTitle())
	assert.Equal(t, "Robert Martin", book.GetAuthor())
	assert.Equal(t, int32(2017), book.GetYear())
	assert.Equal(t, "indexed", book.GetStatus())
	assert.Equal(t, []string{"architecture", "ddd"}, book.GetTags())
	assert.Equal(t, "books/b-1/file.pdf", book.GetS3Key())
	assert.Equal(t, int64(1700000000), book.GetCreatedAt())
	assert.Equal(t, int64(1700000001), book.GetUpdatedAt())
}

func TestGetBookResponse_NilBook_ReturnsNil(t *testing.T) {
	resp := &metadatapb.GetBookResponse{}
	assert.Nil(t, resp.GetBook())
}

func TestMetadataServiceDesc_MethodNames(t *testing.T) {
	desc := metadatapb.MetadataService_ServiceDesc
	assert.Equal(t, "raglibrarian.metadata.v1.MetadataService", desc.ServiceName)
	assert.Len(t, desc.Methods, 2)

	names := map[string]bool{}
	for _, m := range desc.Methods {
		names[m.MethodName] = true
	}
	assert.True(t, names["UpdateBookStatus"])
	assert.True(t, names["GetBook"])
	assert.Empty(t, desc.Streams, "MetadataService has no streaming RPCs")
}

func TestMetadataService_FullMethodNames(t *testing.T) {
	assert.Equal(t,
		"/raglibrarian.metadata.v1.MetadataService/UpdateBookStatus",
		metadatapb.MetadataService_UpdateBookStatus_FullMethodName,
	)
	assert.Equal(t,
		"/raglibrarian.metadata.v1.MetadataService/GetBook",
		metadatapb.MetadataService_GetBook_FullMethodName,
	)
}
