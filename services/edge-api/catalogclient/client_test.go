package catalogclient

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
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
	if _, err = client.UploadBook(ctx, handler.BookMetadata{Title: "Title", Author: "Author", Year: year}, handler.CatalogActor{}, testRequestID, bytes.NewBufferString("%PDF")); err != nil {
		t.Fatalf("upload book: %v", err)
	}
	metadata := <-capture.metadata
	if metadata.Year != year {
		t.Fatalf("forwarded year = %d, want %d", metadata.Year, year)
	}
}

func TestMapErrorMapsCatalogAuthorizationFailure(t *testing.T) {
	err := mapError(status.Error(codes.PermissionDenied, "actor is not authorized"))

	if err != handler.ErrBookUnauthorized {
		t.Fatalf("mapError() = %v, want %v", err, handler.ErrBookUnauthorized)
	}
}
