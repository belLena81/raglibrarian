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
	"github.com/belLena81/raglibrarian/pkg/providerhttp"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/providerstub"
)

var dropPrivileges = process.DropPrivileges

type runtimeConfig struct {
	Address           string
	CertificateFile   string
	KeyFile           string
	APIKeyFile        string
	RunAs             process.Identity
	Delay             time.Duration
	Scenario          providerstub.Scenario
	Policy            providerstub.Policy
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

func main() {
	configuration, err := loadConfig()
	if err != nil {
		log.Fatal("provider stub configuration is invalid")
	}
	apiKey, err := providerhttp.ReadSingleLineSecret(configuration.APIKeyFile, 4096)
	if err != nil {
		log.Fatal("provider stub configuration is invalid")
	}
	keyPair, err := tls.LoadX509KeyPair(configuration.CertificateFile, configuration.KeyFile)
	if err != nil {
		log.Fatal("provider stub configuration is invalid")
	}
	handler, err := providerstub.New(apiKey, configuration.Scenario, configuration.Delay, configuration.Policy)
	if err != nil {
		log.Fatal("provider stub configuration is invalid")
	}
	if err = dropPrivileges(configuration.RunAs); err != nil {
		log.Fatal("provider stub could not reduce privileges")
	}
	listener, err := net.Listen("tcp", configuration.Address)
	if err != nil {
		log.Fatal("provider stub listener failed")
	}
	tlsListener := tls.NewListener(listener, &tls.Config{
		Certificates: []tls.Certificate{keyPair},
		MinVersion:   tls.VersionTLS13,
	})
	server := &http.Server{
		Addr:              configuration.Address,
		Handler:           handler,
		ReadHeaderTimeout: configuration.ReadHeaderTimeout,
		ReadTimeout:       configuration.ReadTimeout,
		WriteTimeout:      configuration.WriteTimeout,
		IdleTimeout:       configuration.IdleTimeout,
	}
	if err = server.Serve(tlsListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal("provider stub listener failed")
	}
}

func loadConfig() (runtimeConfig, error) {
	runAs, err := parseRunAs()
	if err != nil {
		return runtimeConfig{}, err
	}
	delay, err := parseDelay(os.Getenv("ANSWER_STUB_DELAY_MS"))
	if err != nil {
		return runtimeConfig{}, err
	}
	scenario := os.Getenv("ANSWER_STUB_SCENARIO")
	if scenario == "" {
		scenario = string(providerstub.ScenarioSuccess)
	}
	configuration := runtimeConfig{
		Address:         os.Getenv("ANSWER_STUB_ADDR"),
		CertificateFile: os.Getenv("ANSWER_STUB_TLS_CERT_FILE"),
		KeyFile:         os.Getenv("ANSWER_STUB_TLS_KEY_FILE"),
		APIKeyFile:      os.Getenv("ANSWER_STUB_API_KEY_FILE"),
		RunAs:           runAs,
		Delay:           delay,
		Scenario:        providerstub.Scenario(scenario),
		Policy: providerstub.Policy{
			MaximumDelay:       envDuration("ANSWER_STUB_MAX_DELAY", 30*time.Second),
			TimeoutDelay:       envDuration("ANSWER_STUB_TIMEOUT_DELAY", 10*time.Second),
			MaximumRequestBody: envInt64("ANSWER_STUB_MAX_REQUEST_BODY_BYTES", 128<<10),
		},
		ReadHeaderTimeout: envDuration("ANSWER_STUB_READ_HEADER_TIMEOUT", 2*time.Second),
		ReadTimeout:       envDuration("ANSWER_STUB_READ_TIMEOUT", 5*time.Second),
		WriteTimeout:      envDuration("ANSWER_STUB_WRITE_TIMEOUT", 15*time.Second),
		IdleTimeout:       envDuration("ANSWER_STUB_IDLE_TIMEOUT", 30*time.Second),
	}
	if configuration.Address == "" || configuration.CertificateFile == "" || configuration.KeyFile == "" || configuration.APIKeyFile == "" {
		return runtimeConfig{}, errors.New("missing required configuration")
	}
	return configuration, nil
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envInt64(key string, fallback int64) int64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
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
		return 65532, nil
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
