package identityclient

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/belLena81/raglibrarian/pkg/domain"
	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
)

// Client adapts the versioned Identity gRPC API to the edge handler contract.
type Client struct{ rpc identityv1.IdentityServiceClient }

func New(rpc identityv1.IdentityServiceClient) *Client { return &Client{rpc: rpc} }

func (c *Client) Register(ctx context.Context, email, password string, role domain.Role) (string, domain.User, error) {
	response, err := c.rpc.Register(ctx, &identityv1.RegisterRequest{Email: email, Password: password, Role: string(role)})
	if err != nil { return "", domain.User{}, mapError(err) }
	return response.AccessToken, domain.NewUserFromDB("", email, "", domain.Role(response.Role), time.Time{}), nil
}

func (c *Client) Login(ctx context.Context, email, password string) (string, error) {
	response, err := c.rpc.Login(ctx, &identityv1.LoginRequest{Email: email, Password: password})
	if err != nil { return "", mapError(err) }
	return response.AccessToken, nil
}

func mapError(err error) error {
	switch status.Code(err) {
	case codes.AlreadyExists: return domain.ErrEmailTaken
	case codes.InvalidArgument: return domain.ErrInvalidEmail
	case codes.Unauthenticated: return domain.ErrInvalidCredentials
	default: return err
	}
}
