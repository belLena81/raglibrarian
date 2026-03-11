package grpc_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	googlegrpc "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/pkg/proto/metadatapb"
	grpcserver "github.com/belLena81/raglibrarian/services/metadata/grpc"
	metarepo "github.com/belLena81/raglibrarian/services/metadata/repository"
)

// ── fakeBookUseCase ───────────────────────────────────────────────────────────
// Minimal fake driven by per-test error injection.
// The real BookService is covered by its own unit tests; here we verify that
// the server layer correctly translates use-case outcomes into gRPC statuses.

type fakeBookUseCase struct {
	book            domain.Book
	updateStatusErr error
	getBookErr      error
}

func (f *fakeBookUseCase) AddBook(_ context.Context, _, _ string, _ int) (domain.Book, error) {
	return domain.Book{}, nil
}
func (f *fakeBookUseCase) GetBook(_ context.Context, _ string) (domain.Book, error) {
	return f.book, f.getBookErr
}
func (f *fakeBookUseCase) ListBooks(_ context.Context, _ metarepo.ListFilter) ([]domain.Book, error) {
	return nil, nil
}
func (f *fakeBookUseCase) RemoveBook(_ context.Context, _ string) error { return nil }
func (f *fakeBookUseCase) UpdateStatus(_ context.Context, _ string, _ domain.Status) error {
	return f.updateStatusErr
}
func (f *fakeBookUseCase) TriggerReindex(_ context.Context, _ string) error { return nil }

// Compile-time check.
var _ grpcserver.BookUseCaseInterface = (*fakeBookUseCase)(nil)

// ── helpers ───────────────────────────────────────────────────────────────────

func newServer(uc grpcserver.BookUseCaseInterface) *grpcserver.MetadataServer {
	return grpcserver.NewMetadataServer(uc)
}

func grpcCode(err error) googlegrpc.Code {
	return status.Code(err)
}

// ── Constructor ───────────────────────────────────────────────────────────────

func TestNewMetadataServer_NilUseCase_Panics(t *testing.T) {
	assert.Panics(t, func() { grpcserver.NewMetadataServer(nil) })
}

// ── UpdateBookStatus ──────────────────────────────────────────────────────────

func TestUpdateBookStatus_ValidTransition_ReturnsNewStatus(t *testing.T) {
	srv := newServer(&fakeBookUseCase{})
	req := &metadatapb.UpdateBookStatusRequest{BookId: "b-1", Status: "indexing"}

	resp, err := srv.UpdateBookStatus(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "b-1", resp.GetBookId())
	assert.Equal(t, "indexing", resp.GetStatus())
}

func TestUpdateBookStatus_EmptyBookID_ReturnsInvalidArgument(t *testing.T) {
	srv := newServer(&fakeBookUseCase{})
	req := &metadatapb.UpdateBookStatusRequest{BookId: "", Status: "indexing"}

	_, err := srv.UpdateBookStatus(context.Background(), req)

	assert.Equal(t, googlegrpc.InvalidArgument, grpcCode(err))
}

func TestUpdateBookStatus_EmptyStatus_ReturnsInvalidArgument(t *testing.T) {
	srv := newServer(&fakeBookUseCase{})
	req := &metadatapb.UpdateBookStatusRequest{BookId: "b-1", Status: ""}

	_, err := srv.UpdateBookStatus(context.Background(), req)

	assert.Equal(t, googlegrpc.InvalidArgument, grpcCode(err))
}

func TestUpdateBookStatus_UnrecognisedStatus_ReturnsInvalidArgument(t *testing.T) {
	srv := newServer(&fakeBookUseCase{
		updateStatusErr: domain.ErrInvalidStatus,
	})
	req := &metadatapb.UpdateBookStatusRequest{BookId: "b-1", Status: "garbage"}

	_, err := srv.UpdateBookStatus(context.Background(), req)

	assert.Equal(t, googlegrpc.InvalidArgument, grpcCode(err))
}

func TestUpdateBookStatus_ForbiddenTransition_ReturnsFailedPrecondition(t *testing.T) {
	srv := newServer(&fakeBookUseCase{
		updateStatusErr: domain.ErrInvalidStatusTransition,
	})
	req := &metadatapb.UpdateBookStatusRequest{BookId: "b-1", Status: "indexed"}

	_, err := srv.UpdateBookStatus(context.Background(), req)

	assert.Equal(t, googlegrpc.FailedPrecondition, grpcCode(err))
}

func TestUpdateBookStatus_BookNotFound_ReturnsNotFound(t *testing.T) {
	srv := newServer(&fakeBookUseCase{
		updateStatusErr: domain.ErrBookNotFound,
	})
	req := &metadatapb.UpdateBookStatusRequest{BookId: "ghost", Status: "indexing"}

	_, err := srv.UpdateBookStatus(context.Background(), req)

	assert.Equal(t, googlegrpc.NotFound, grpcCode(err))
}

func TestUpdateBookStatus_UnexpectedError_ReturnsInternal(t *testing.T) {
	srv := newServer(&fakeBookUseCase{
		updateStatusErr: assert.AnError,
	})
	req := &metadatapb.UpdateBookStatusRequest{BookId: "b-1", Status: "indexing"}

	_, err := srv.UpdateBookStatus(context.Background(), req)

	assert.Equal(t, googlegrpc.Internal, grpcCode(err))
}

// ── GetBook ───────────────────────────────────────────────────────────────────

func TestGetBook_Exists_ReturnsBookProto(t *testing.T) {
	b, err := domain.NewBook("Clean Architecture", "Robert Martin", 2017)
	require.NoError(t, err)

	srv := newServer(&fakeBookUseCase{book: b})
	req := &metadatapb.GetBookRequest{BookId: b.Id()}

	resp, err := srv.GetBook(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp.GetBook())
	assert.Equal(t, b.Id(), resp.GetBook().GetBookId())
	assert.Equal(t, "Clean Architecture", resp.GetBook().GetTitle())
	assert.Equal(t, "Robert Martin", resp.GetBook().GetAuthor())
	assert.Equal(t, int32(2017), resp.GetBook().GetYear())
	assert.Equal(t, "pending", resp.GetBook().GetStatus())
	assert.NotZero(t, resp.GetBook().GetCreatedAt())
	assert.NotZero(t, resp.GetBook().GetUpdatedAt())
}

func TestGetBook_EmptyID_ReturnsInvalidArgument(t *testing.T) {
	srv := newServer(&fakeBookUseCase{})

	_, err := srv.GetBook(context.Background(), &metadatapb.GetBookRequest{BookId: ""})

	assert.Equal(t, googlegrpc.InvalidArgument, grpcCode(err))
}

func TestGetBook_Missing_ReturnsNotFound(t *testing.T) {
	srv := newServer(&fakeBookUseCase{getBookErr: domain.ErrBookNotFound})

	_, err := srv.GetBook(context.Background(), &metadatapb.GetBookRequest{BookId: "ghost"})

	assert.Equal(t, googlegrpc.NotFound, grpcCode(err))
}

func TestGetBook_UnexpectedError_ReturnsInternal(t *testing.T) {
	srv := newServer(&fakeBookUseCase{getBookErr: assert.AnError})

	_, err := srv.GetBook(context.Background(), &metadatapb.GetBookRequest{BookId: "b-1"})

	assert.Equal(t, googlegrpc.Internal, grpcCode(err))
}

// ── BookProto field mapping ───────────────────────────────────────────────────

func TestGetBook_TagsAndS3Key_MappedCorrectly(t *testing.T) {
	b, err := domain.NewBook("DDIA", "Kleppmann", 2017)
	require.NoError(t, err)
	require.NoError(t, b.SetTags([]string{"databases", "distributed"}))
	require.NoError(t, b.SetS3Key("books/ddia/file.pdf"))

	srv := newServer(&fakeBookUseCase{book: b})

	resp, err := srv.GetBook(context.Background(), &metadatapb.GetBookRequest{BookId: b.Id()})

	require.NoError(t, err)
	assert.Equal(t, []string{"databases", "distributed"}, resp.GetBook().GetTags())
	assert.Equal(t, "books/ddia/file.pdf", resp.GetBook().GetS3Key())
}

func TestGetBook_NilTags_MappedToEmpty(t *testing.T) {
	// A freshly-constructed book has empty tags — proto should reflect that
	// as an empty repeated field (nil in Go, not a nil pointer book).
	b, err := domain.NewBook("Title", "Author", 2020)
	require.NoError(t, err)

	srv := newServer(&fakeBookUseCase{book: b})

	resp, err := srv.GetBook(context.Background(), &metadatapb.GetBookRequest{BookId: b.Id()})

	require.NoError(t, err)
	// proto3 empty repeated field is nil; callers must treat nil == empty.
	assert.Empty(t, resp.GetBook().GetTags())
}
