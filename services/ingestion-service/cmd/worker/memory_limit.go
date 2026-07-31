package main

import (
	"github.com/belLena81/raglibrarian/services/ingestion-service/config"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/memorybudget"
)

func validateRuntimeMemoryBudget(cfg config.Config) error {
	return memorybudget.Validate(cfg.WorkConcurrency, cfg.ParserSandboxMemoryBytes, cfg.ParserRuntimeHeadroomBytes)
}
