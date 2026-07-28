package rpcpolicy

import "time"

const (
	MaximumAnswerDeadline          = 5 * time.Minute
	MaximumRetrievalSearchDeadline = 5 * time.Minute
	MaximumCatalogPreviewDeadline  = 30 * time.Second
)
