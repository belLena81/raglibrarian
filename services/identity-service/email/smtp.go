// Package email implements Identity's outbound verification-email adapter.
package email

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// ErrDeliveryFailed is the sanitized error returned for every SMTP delivery
// failure so transport details and recipient data never cross the adapter.
var ErrDeliveryFailed = errors.New("email delivery failed")

const smtpOperationTimeout = 10 * time.Second

// Config contains the SMTP endpoint and verification-message settings.
type Config struct {
	Address    string
	ServerName string
	Username   string
	Password   string
	From       string
	VerifyURL  string
	StartTLS   bool
}

// SMTPSender delivers verification messages through an SMTP server.
type SMTPSender struct{ config Config }

// NewSMTPSender validates configuration and constructs an SMTP adapter.
func NewSMTPSender(config Config) (*SMTPSender, error) {
	if config.Address == "" || config.ServerName == "" || config.From == "" || config.VerifyURL == "" {
		return nil, ErrDeliveryFailed
	}
	if config.Username != "" && (!config.StartTLS || config.Password == "") {
		return nil, ErrDeliveryFailed
	}
	return &SMTPSender{config: config}, nil
}

// SendVerification sends one bounded verification message to recipient.
func (s *SMTPSender) SendVerification(ctx context.Context, recipient, token string) error {
	if recipient == "" || token == "" || strings.ContainsAny(recipient, "\r\n") {
		return ErrDeliveryFailed
	}
	operationCtx, cancel := context.WithTimeout(ctx, smtpOperationTimeout)
	defer cancel()
	dialer := net.Dialer{Timeout: smtpOperationTimeout}
	connection, err := dialer.DialContext(operationCtx, "tcp", s.config.Address)
	if err != nil {
		return ErrDeliveryFailed
	}
	deadline, ok := operationCtx.Deadline()
	if !ok || connection.SetDeadline(deadline) != nil {
		_ = connection.Close()
		return ErrDeliveryFailed
	}
	client, err := smtp.NewClient(connection, s.config.ServerName)
	if err != nil {
		_ = connection.Close()
		return ErrDeliveryFailed
	}
	defer func() { _ = client.Close() }()
	if s.config.StartTLS {
		if err = client.StartTLS(&tls.Config{ServerName: s.config.ServerName, MinVersion: tls.VersionTLS12}); err != nil {
			return ErrDeliveryFailed
		}
	}
	if s.config.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", s.config.Username, s.config.Password, s.config.ServerName)); err != nil {
			return ErrDeliveryFailed
		}
	}
	if err = client.Mail(s.config.From); err != nil {
		return ErrDeliveryFailed
	}
	if err = client.Rcpt(recipient); err != nil {
		return ErrDeliveryFailed
	}
	wc, err := client.Data()
	if err != nil {
		return ErrDeliveryFailed
	}
	message := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: Verify your raglibrarian registration\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nOpen this link to verify your registration:\r\n%s#%s\r\n",
		s.config.From, recipient, strings.TrimRight(s.config.VerifyURL, "#"), token,
	)
	if _, err = wc.Write([]byte(message)); err != nil {
		_ = wc.Close()
		return ErrDeliveryFailed
	}
	if err = wc.Close(); err != nil {
		return ErrDeliveryFailed
	}
	if err = client.Quit(); err != nil {
		return ErrDeliveryFailed
	}
	return nil
}
