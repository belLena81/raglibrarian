package catalogclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	grpcmetadata "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
)

const testRequestID = "0123456789abcdef0123456789abcdef"

type uploadCaptureServer struct {
	catalogv1.UnimplementedCatalogServiceServer
	metadata chan *catalogv1.UploadBookMetadata
}

func (s *uploadCaptureServer) UploadBook(stream grpc.ClientStreamingServer[catalogv1.UploadBookRequest, catalogv1.UploadBookResponse]) error {
	for {
		request, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&catalogv1.UploadBookResponse{Book: &catalogv1.Book{Id: "book-id"}})
		}
		if err != nil {
			return err
		}
		if metadata := request.GetMetadata(); metadata != nil {
			s.metadata <- metadata
		}
	}
}

func TestUploadBookForwardsValidatedYearUnchanged(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	capture := &uploadCaptureServer{metadata: make(chan *catalogv1.UploadBookMetadata, 1)}
	catalogv1.RegisterCatalogServiceServer(server, capture)
	go func() {
		if err := server.Serve(listener); err != nil {
			return
		}
	}()
	t.Cleanup(server.Stop)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	connection, err := grpc.NewClient("passthrough:///bufconn", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("connect to Catalog test server: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := connection.Close(); closeErr != nil {
			t.Errorf("close Catalog test connection: %v", closeErr)
		}
	})

	const year int32 = 2027
	client := New(catalogv1.NewCatalogServiceClient(connection))
	if _, err = client.UploadBook(ctx, handler.BookMetadata{Title: "Title", Author: "Author", Year: year, MediaType: "application/epub+zip"}, handler.CatalogActor{}, testRequestID, bytes.NewBufferString("PK")); err != nil {
		t.Fatalf("upload book: %v", err)
	}
	metadata := <-capture.metadata
	if metadata.Year != year {
		t.Fatalf("forwarded year = %d, want %d", metadata.Year, year)
	}
	if metadata.MediaType != "application/epub+zip" {
		t.Fatalf("forwarded media type = %q", metadata.MediaType)
	}
}

func TestMapErrorMapsCatalogAuthorizationFailure(t *testing.T) {
	err := mapError(status.Error(codes.PermissionDenied, "actor is not authorized"))

	if err != handler.ErrBookUnauthorized {
		t.Fatalf("mapError() = %v, want %v", err, handler.ErrBookUnauthorized)
	}
}

type catalogClientStub struct {
	catalogv1.CatalogServiceClient
	upload  func(context.Context, ...grpc.CallOption) (grpc.ClientStreamingClient[catalogv1.UploadBookRequest, catalogv1.UploadBookResponse], error)
	list    func(context.Context, *catalogv1.ListBooksRequest, ...grpc.CallOption) (*catalogv1.ListBooksResponse, error)
	get     func(context.Context, *catalogv1.GetBookRequest, ...grpc.CallOption) (*catalogv1.GetBookResponse, error)
	reindex func(context.Context, *catalogv1.ReindexBookRequest, ...grpc.CallOption) (*catalogv1.ReindexBookResponse, error)
	delete  func(context.Context, *catalogv1.DeleteBookRequest, ...grpc.CallOption) (*catalogv1.DeleteBookResponse, error)
}

func (s *catalogClientStub) UploadBook(ctx context.Context, opts ...grpc.CallOption) (grpc.ClientStreamingClient[catalogv1.UploadBookRequest, catalogv1.UploadBookResponse], error) {
	return s.upload(ctx, opts...)
}

func (s *catalogClientStub) ListBooks(ctx context.Context, request *catalogv1.ListBooksRequest, opts ...grpc.CallOption) (*catalogv1.ListBooksResponse, error) {
	return s.list(ctx, request, opts...)
}

func (s *catalogClientStub) GetBook(ctx context.Context, request *catalogv1.GetBookRequest, opts ...grpc.CallOption) (*catalogv1.GetBookResponse, error) {
	return s.get(ctx, request, opts...)
}
func (s *catalogClientStub) ReindexBook(ctx context.Context, request *catalogv1.ReindexBookRequest, opts ...grpc.CallOption) (*catalogv1.ReindexBookResponse, error) {
	return s.reindex(ctx, request, opts...)
}
func (s *catalogClientStub) DeleteBook(ctx context.Context, request *catalogv1.DeleteBookRequest, opts ...grpc.CallOption) (*catalogv1.DeleteBookResponse, error) {
	return s.delete(ctx, request, opts...)
}

func TestLifecycleCommandsForwardActorCommandAndProjection(t *testing.T) {
	service := &catalogClientStub{
		reindex: func(_ context.Context, request *catalogv1.ReindexBookRequest, _ ...grpc.CallOption) (*catalogv1.ReindexBookResponse, error) {
			if request.CommandId != "reindex-command" || request.Actor.GetUserId() != "manager" {
				t.Fatalf("unexpected reindex request: %+v", request)
			}
			return &catalogv1.ReindexBookResponse{Book: &catalogv1.Book{Id: request.BookId, MediaType: "application/epub+zip", LifecycleVersion: 4, CanReindex: false}}, nil
		},
		delete: func(_ context.Context, request *catalogv1.DeleteBookRequest, _ ...grpc.CallOption) (*catalogv1.DeleteBookResponse, error) {
			if request.CommandId != "delete-command" || request.Actor.GetUserId() != "manager" {
				t.Fatalf("unexpected delete request: %+v", request)
			}
			return &catalogv1.DeleteBookResponse{Book: &catalogv1.Book{Id: request.BookId, LifecycleVersion: 5}}, nil
		},
	}
	client := New(service)
	actor := handler.CatalogActor{UserID: "manager"}

	reindexed, err := client.ReindexBook(context.Background(), "book-id", actor, testRequestID, "reindex-command")
	if err != nil || reindexed.MediaType != "application/epub+zip" || reindexed.LifecycleVersion != 4 {
		t.Fatalf("ReindexBook() = (%+v, %v)", reindexed, err)
	}
	deleted, err := client.DeleteBook(context.Background(), "book-id", actor, testRequestID, "delete-command")
	if err != nil || deleted.LifecycleVersion != 5 {
		t.Fatalf("DeleteBook() = (%+v, %v)", deleted, err)
	}
}

type earlyClosedUploadStream struct {
	grpc.ClientStream
	sends      int
	closeCalls int
	failAt     int
	sendErr    error
	terminal   error
}

func (s *earlyClosedUploadStream) Send(*catalogv1.UploadBookRequest) error {
	s.sends++
	if s.sends == s.failAt {
		return s.sendErr
	}
	return nil
}

func (s *earlyClosedUploadStream) CloseAndRecv() (*catalogv1.UploadBookResponse, error) {
	s.closeCalls++
	return nil, s.terminal
}

func TestUploadBookRecoversTerminalStatusAfterSendEOF(t *testing.T) {
	tests := []struct {
		name     string
		failAt   int
		terminal error
		want     error
	}{
		{name: "oversized upload after metadata send", failAt: 1, terminal: status.Error(codes.ResourceExhausted, "upload too large"), want: handler.ErrBookTooLarge},
		{name: "capacity exhausted after chunk send", failAt: 2, terminal: status.Error(codes.ResourceExhausted, "upload capacity exhausted"), want: handler.ErrBookCapacityExhausted},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := &earlyClosedUploadStream{failAt: test.failAt, sendErr: io.EOF, terminal: test.terminal}
			service := &catalogClientStub{upload: func(context.Context, ...grpc.CallOption) (grpc.ClientStreamingClient[catalogv1.UploadBookRequest, catalogv1.UploadBookResponse], error) {
				return stream, nil
			}}

			_, err := New(service).UploadBook(context.Background(), handler.BookMetadata{}, handler.CatalogActor{}, testRequestID, bytes.NewBufferString("%PDF"))

			if !errors.Is(err, test.want) {
				t.Fatalf("UploadBook() error = %v, want %v", err, test.want)
			}
			if stream.closeCalls != 1 {
				t.Fatalf("CloseAndRecv calls = %d, want 1", stream.closeCalls)
			}
		})
	}
}

func TestUploadBookMapsNonEOFSendStatusWithoutClosingAgain(t *testing.T) {
	stream := &earlyClosedUploadStream{failAt: 1, sendErr: status.Error(codes.PermissionDenied, "actor is not authorized")}
	service := &catalogClientStub{upload: func(context.Context, ...grpc.CallOption) (grpc.ClientStreamingClient[catalogv1.UploadBookRequest, catalogv1.UploadBookResponse], error) {
		return stream, nil
	}}

	_, err := New(service).UploadBook(context.Background(), handler.BookMetadata{}, handler.CatalogActor{}, testRequestID, bytes.NewBufferString("%PDF"))

	if !errors.Is(err, handler.ErrBookUnauthorized) {
		t.Fatalf("UploadBook() error = %v, want %v", err, handler.ErrBookUnauthorized)
	}
	if stream.closeCalls != 0 {
		t.Fatalf("CloseAndRecv calls = %d, want 0", stream.closeCalls)
	}
}

func TestCatalogReadsForwardValidatedRequestID(t *testing.T) {
	var listRequestIDs []string
	var getRequestIDs []string
	service := &catalogClientStub{
		list: func(ctx context.Context, _ *catalogv1.ListBooksRequest, _ ...grpc.CallOption) (*catalogv1.ListBooksResponse, error) {
			metadata, _ := grpcmetadata.FromOutgoingContext(ctx)
			listRequestIDs = metadata.Get("x-request-id")
			return &catalogv1.ListBooksResponse{}, nil
		},
		get: func(ctx context.Context, _ *catalogv1.GetBookRequest, _ ...grpc.CallOption) (*catalogv1.GetBookResponse, error) {
			metadata, _ := grpcmetadata.FromOutgoingContext(ctx)
			getRequestIDs = metadata.Get("x-request-id")
			return &catalogv1.GetBookResponse{Book: &catalogv1.Book{}}, nil
		},
	}
	ctx := grpcmetadata.NewOutgoingContext(context.Background(), grpcmetadata.Pairs(
		"x-request-id", "stale-request-id",
		"x-request-id", "duplicate-request-id",
	))
	ctx = context.WithValue(ctx, chimiddleware.RequestIDKey, testRequestID)
	client := New(service)

	if _, err := client.ListBooks(ctx, 25, "", handler.CatalogActor{}); err != nil {
		t.Fatalf("ListBooks() error = %v", err)
	}
	if _, err := client.GetBook(ctx, "AAAAAAAAAAAAAAAAAAAAAA", handler.CatalogActor{}); err != nil {
		t.Fatalf("GetBook() error = %v", err)
	}
	if len(listRequestIDs) != 1 || listRequestIDs[0] != testRequestID {
		t.Fatalf("ListBooks request IDs = %q, want [%q]", listRequestIDs, testRequestID)
	}
	if len(getRequestIDs) != 1 || getRequestIDs[0] != testRequestID {
		t.Fatalf("GetBook request IDs = %q, want [%q]", getRequestIDs, testRequestID)
	}
}

func TestCatalogReadsRejectInvalidInternalRequestIDBeforeRPC(t *testing.T) {
	listCalls := 0
	getCalls := 0
	service := &catalogClientStub{
		list: func(context.Context, *catalogv1.ListBooksRequest, ...grpc.CallOption) (*catalogv1.ListBooksResponse, error) {
			listCalls++
			return &catalogv1.ListBooksResponse{}, nil
		},
		get: func(context.Context, *catalogv1.GetBookRequest, ...grpc.CallOption) (*catalogv1.GetBookResponse, error) {
			getCalls++
			return &catalogv1.GetBookResponse{}, nil
		},
	}

	for _, requestID := range []string{"", strings.Repeat("a", 31), strings.Repeat("A", 32), strings.Repeat("z", 32)} {
		ctx := context.WithValue(context.Background(), chimiddleware.RequestIDKey, requestID)
		client := New(service)
		if _, err := client.ListBooks(ctx, 25, "", handler.CatalogActor{}); !errors.Is(err, handler.ErrInvalidBookRequest) {
			t.Fatalf("ListBooks() with request ID %q error = %v, want invalid request", requestID, err)
		}
		if _, err := client.GetBook(ctx, "AAAAAAAAAAAAAAAAAAAAAA", handler.CatalogActor{}); !errors.Is(err, handler.ErrInvalidBookRequest) {
			t.Fatalf("GetBook() with request ID %q error = %v, want invalid request", requestID, err)
		}
	}
	if listCalls != 0 || getCalls != 0 {
		t.Fatalf("Catalog read calls = list %d, get %d; want zero", listCalls, getCalls)
	}
}
