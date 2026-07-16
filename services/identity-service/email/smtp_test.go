package email

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewSMTPSenderRejectsAuthenticationWithoutTLS(t *testing.T) {
	_, err := NewSMTPSender(Config{
		Address: "smtp.example.test:25", ServerName: "smtp.example.test",
		Username: "identity", Password: "secret", From: "no-reply@example.test",
		VerifyURL: "https://example.test/verify-email",
	})
	require.ErrorIs(t, err, ErrDeliveryFailed)
}

func TestNewSMTPSenderAllowsUnauthenticatedLocalAdapter(t *testing.T) {
	_, err := NewSMTPSender(Config{
		Address: "mailpit:1025", ServerName: "mailpit",
		From: "no-reply@example.test", VerifyURL: "http://localhost:5173/verify-email",
	})
	require.NoError(t, err)
}

func TestSMTPSenderHonorsContextWhileWaitingForServerGreeting(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		<-ctx.Done()
	}()

	sender, err := NewSMTPSender(Config{
		Address: listener.Addr().String(), ServerName: "localhost",
		From: "no-reply@example.test", VerifyURL: "https://example.test/verify-email",
	})
	require.NoError(t, err)

	started := time.Now()
	err = sender.SendVerification(ctx, "reader@example.test", "verification-token")

	require.ErrorIs(t, err, ErrDeliveryFailed)
	require.Less(t, time.Since(started), time.Second)
	<-serverDone
}
