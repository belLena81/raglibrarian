// Package catalogclient contains Edge's gRPC adapter for Catalog.
package catalogclient

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	grpcmetadata "google.golang.org/grpc/metadata"
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

func (c *Client) UploadBook(ctx context.Context, metadata handler.BookMetadata, actor handler.CatalogActor, correlationID string, reader io.Reader) (handler.Book, error) {
	if !validRequestID(correlationID) {
		return handler.Book{}, handler.ErrInvalidBookRequest
	}
	ctx = grpcmetadata.AppendToOutgoingContext(ctx, "x-request-id", correlationID)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	stream, err := c.service.UploadBook(ctx)
	if err != nil {
		return handler.Book{}, err
	}
	if err = stream.Send(&catalogv1.UploadBookRequest{Frame: &catalogv1.UploadBookRequest_Metadata{Metadata: &catalogv1.UploadBookMetadata{Title: metadata.Title, Author: metadata.Author, Year: int32(metadata.Year), Tags: metadata.Tags, Actor: actorProto(actor)}}}); err != nil {
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

func validRequestID(value string) bool {
	if len(value) != 32 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}
func (c *Client) ListBooks(ctx context.Context, size int, token string, actor handler.CatalogActor) (handler.BookPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response, err := c.service.ListBooks(ctx, &catalogv1.ListBooksRequest{PageSize: int32(size), PageToken: token, Actor: actorProto(actor)})
	if err != nil {
		return handler.BookPage{}, mapError(err)
	}
	books := make([]handler.Book, 0, len(response.Books))
	for _, book := range response.Books {
		books = append(books, fromProto(book))
	}
	return handler.BookPage{Books: books, NextPageToken: response.NextPageToken}, nil
}
func (c *Client) GetBook(ctx context.Context, id string, actor handler.CatalogActor) (handler.Book, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response, err := c.service.GetBook(ctx, &catalogv1.GetBookRequest{BookId: id, Actor: actorProto(actor)})
	if err != nil {
		return handler.Book{}, mapError(err)
	}
	return fromProto(response.Book), nil
}
func actorProto(actor handler.CatalogActor) *catalogv1.Actor {
	return &catalogv1.Actor{UserId: actor.UserID, Role: actor.Role, Status: actor.Status, MaskedEmail: actor.MaskedEmail}
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
	if status.Code(err) == codes.ResourceExhausted && status.Convert(err).Message() == "upload capacity exhausted" {
		return handler.ErrBookCapacityExhausted
	}
	if status.Code(err) == codes.ResourceExhausted {
		return handler.ErrBookTooLarge
	}
	if status.Code(err) == codes.InvalidArgument {
		if status.Convert(err).Message() == "invalid pagination" {
			return handler.ErrInvalidPagination
		}
		return handler.ErrInvalidBookRequest
	}
	return err
}
