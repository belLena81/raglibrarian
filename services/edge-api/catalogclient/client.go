// Package catalogclient contains Edge's gRPC adapter for Catalog.
package catalogclient

import (
	"context"
	"errors"
	"io"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
)

type Client struct {
	service catalogv1.CatalogServiceClient
}

func New(service catalogv1.CatalogServiceClient) *Client { return &Client{service: service} }
func (c *Client) CheckReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := c.service.Check(ctx, &catalogv1.CheckRequest{})
	return err
}
func (c *Client) UploadBook(ctx context.Context, metadata handler.BookMetadata, actorID, correlationID string, reader io.Reader) (handler.Book, error) {
	stream, err := c.service.UploadBook(ctx)
	if err != nil {
		return handler.Book{}, err
	}
	if err = stream.Send(&catalogv1.UploadBookRequest{Frame: &catalogv1.UploadBookRequest_Metadata{Metadata: &catalogv1.UploadBookMetadata{Title: metadata.Title, Author: metadata.Author, Year: int32(metadata.Year), Tags: metadata.Tags, ActorId: actorID, CorrelationId: correlationID}}}); err != nil {
		return handler.Book{}, err
	}
	buffer := make([]byte, 64<<10)
	for {
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if err = stream.Send(&catalogv1.UploadBookRequest{Frame: &catalogv1.UploadBookRequest_Chunk{Chunk: append([]byte(nil), buffer[:n]...)}}); err != nil {
				return handler.Book{}, err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return handler.Book{}, readErr
		}
	}
	response, err := stream.CloseAndRecv()
	if err != nil {
		return handler.Book{}, mapError(err)
	}
	return fromProto(response.Book), nil
}
func (c *Client) ListBooks(ctx context.Context, size int, token string) (handler.BookPage, error) {
	response, err := c.service.ListBooks(ctx, &catalogv1.ListBooksRequest{PageSize: int32(size), PageToken: token})
	if err != nil {
		return handler.BookPage{}, mapError(err)
	}
	books := make([]handler.Book, 0, len(response.Books))
	for _, book := range response.Books {
		books = append(books, fromProto(book))
	}
	return handler.BookPage{Books: books, NextPageToken: response.NextPageToken}, nil
}
func (c *Client) GetBook(ctx context.Context, id string) (handler.Book, error) {
	response, err := c.service.GetBook(ctx, &catalogv1.GetBookRequest{BookId: id})
	if err != nil {
		return handler.Book{}, mapError(err)
	}
	return fromProto(response.Book), nil
}
func fromProto(book *catalogv1.Book) handler.Book {
	if book == nil {
		return handler.Book{}
	}
	createdAt := time.Time{}
	if book.CreatedAt != nil {
		createdAt = book.CreatedAt.AsTime()
	}
	return handler.Book{ID: book.Id, Title: book.Title, Author: book.Author, Year: int(book.Year), Tags: append([]string(nil), book.Tags...), ProcessingStatus: book.ProcessingStatus, CreatedAt: createdAt}
}
func mapError(err error) error {
	if status.Code(err) == codes.NotFound {
		return handler.ErrBookNotFound
	}
	if status.Code(err) == codes.InvalidArgument || status.Code(err) == codes.ResourceExhausted {
		return handler.ErrInvalidBookRequest
	}
	return err
}
