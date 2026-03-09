// Command query starts the raglibrarian query service.
package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/belLena81/raglibrarian/services/query"
	"github.com/belLena81/raglibrarian/services/query/handler"
	"github.com/belLena81/raglibrarian/services/query/repository"
	"github.com/belLena81/raglibrarian/services/query/usecase"
)

func main() {
	addr := envOrDefault("QUERY_ADDR", ":8080")

	// Wiring: infrastructure → use case → handler → router.
	// Each layer depends only on its immediate neighbour via an interface.
	repo := repository.NewStubQueryRepository()
	svc := usecase.NewQueryService(repo)
	qh := handler.NewQueryHandler(svc)
	router := query.NewRouter(qh)

	fmt.Printf("query service listening on %s\n", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
