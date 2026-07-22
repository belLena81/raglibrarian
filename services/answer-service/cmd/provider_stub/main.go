package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/belLena81/raglibrarian/services/answer-service/internal/provider"
	"github.com/belLena81/raglibrarian/services/answer-service/internal/providerstub"
)

func main() {
	address := os.Getenv("ANSWER_STUB_ADDR")
	certificate := os.Getenv("ANSWER_STUB_TLS_CERT_FILE")
	keyFile := os.Getenv("ANSWER_STUB_TLS_KEY_FILE")
	apiKey, err := provider.ReadAPIKey(os.Getenv("ANSWER_STUB_API_KEY_FILE"))
	if address == "" || certificate == "" || keyFile == "" || err != nil {
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
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second}
	if err = server.ListenAndServeTLS(certificate, keyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal("provider stub listener failed")
	}
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
