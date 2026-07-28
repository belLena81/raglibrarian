package contracts

const (
	ExchangeEvents          = "raglibrarian.events.v1"
	ExchangeIngestionEvents = "raglibrarian.ingestion.events.v1"
	ExchangeEdgeStatus      = "raglibrarian.edge-status.v1"
	ExchangeRetrievalEvents = "raglibrarian.retrieval.events.v1"
	ExchangeRetrievalRetry  = "raglibrarian.retrieval.retry.v1"

	QueueRetrievalMetadata  = "retrieval.book-uploaded.v1"
	QueueRetrievalManifest  = "retrieval.chunks-ready.v1"
	QueueRetrievalIndex     = "retrieval.index-batch.v1"
	QueueRetrievalLifecycle = "retrieval.book-lifecycle.v1"

	EventCatalogBookUploaded               = "catalog.book.uploaded.v1"
	EventCatalogBookReindexRequested       = "catalog.book.reindex-requested.v1"
	EventCatalogBookDeletionRequested      = "catalog.book.deletion-requested.v1"
	EventCatalogBookProcessingStatusChange = "catalog.book.processing-status-changed.v1"

	EventIngestionBookProcessingStarted = "ingestion.book.processing-started.v1"
	EventIngestionBookChunksReady       = "ingestion.book.chunks-ready.v1"
	EventIngestionBookProcessingFailed  = "ingestion.book.processing-failed.v1"
	EventIngestionBookArtifactsDeleted  = "ingestion.book.artifacts-deleted.v1"

	EventRetrievalIndexBatch = "retrieval.index-batch.v1"
)
