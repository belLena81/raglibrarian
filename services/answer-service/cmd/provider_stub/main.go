package main

import (
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/belLena81/raglibrarian/pkg/process"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/provider"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/providerstub"
)

const defaultRuntimeID = 65532

var dropPrivileges = process.DropPrivileges

func main() {
	address := os.Getenv("ANSWER_STUB_ADDR")
	certificate := os.Getenv("ANSWER_STUB_TLS_CERT_FILE")
	keyFile := os.Getenv("ANSWER_STUB_TLS_KEY_FILE")
	apiKey, err := provider.ReadAPIKey(os.Getenv("ANSWER_STUB_API_KEY_FILE"))
	if address == "" || certificate == "" || keyFile == "" || err != nil {
		log.Fatal("provider stub configuration is invalid")
	}
	keyPair, err := tls.LoadX509KeyPair(certificate, keyFile)
	if err != nil {
		log.Fatal("provider stub configuration is invalid")
	}
	runAs, err := parseRunAs()
	if err != nil {
		log.Fatal("provider stub configuration is invalid")
	}
	delay, err := parseDelay(os.Getenv("ANSWER_STUB_DELAY_MS"))
	if err != nil {
		log.Fatal("provider stub configuration is invalid")
	}
	scenario := os.Getenv("ANSWER_STUB_SCENARIO")
	if scenario == "" {
		scenario = string(providerstub.ScenarioSuccess)
	}
	handler, err := providerstub.New(apiKey, providerstub.Scenario(scenario), delay)
	if err != nil {
		log.Fatal("provider stub configuration is invalid")
	}
	if err = dropPrivileges(runAs); err != nil {
		log.Fatal("provider stub could not reduce privileges")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatal("provider stub listener failed")
	}
	tlsListener := tls.NewListener(listener, &tls.Config{
		Certificates: []tls.Certificate{keyPair},
		MinVersion:   tls.VersionTLS13,
	})
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second}
	if err = server.Serve(tlsListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal("provider stub listener failed")
	}
}

func parseRunAs() (process.Identity, error) {
	uid, err := positiveIdentity("RUN_AS_UID")
	if err != nil {
		return process.Identity{}, err
	}
	gid, err := positiveIdentity("RUN_AS_GID")
	if err != nil {
		return process.Identity{}, err
	}
	return process.Identity{UID: uid, GID: gid}, nil
}

func positiveIdentity(name string) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return defaultRuntimeID, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil || parsed < 1 {
		return 0, errors.New("invalid runtime identity")
	}
	return int(parsed), nil
}

func parseDelay(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	milliseconds, err := strconv.Atoi(value)
	if err != nil || milliseconds < 0 || milliseconds > 30000 {
		return 0, errors.New("invalid delay")
	}
	return time.Duration(milliseconds) * time.Millisecond, nil
}
