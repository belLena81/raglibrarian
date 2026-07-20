# M5 Retrieval preparation deployment

This SAM stack owns only the mutually exclusive preparation adapters. Private
PostgreSQL, Amazon MQ, Ingestion artifacts, Retrieval Search, warm TEI and
single-node Qdrant are shared runtime inputs provisioned by the platform stack.
They remain running in every preparation mode.

AWS uses only `lambda -> paused` or `paused -> lambda`. The portable worker is
the local Compose/CI substitute and is never deployed by this stack. The
protected deployment workflow must wait until all three Lambda mappings and
scheduled functions are absent before leaving paused mode.

Lambda mode enables two planner mappings, one index mapping, the outbox
dispatcher schedule, and cleanup schedule. Each mapping has batch size one.
Paused mode disables every preparation Lambda. Local Compose enables exactly
one `cmd/worker` process and contains no preparation Lambda.

All image parameters require immutable ECR digests. Network security groups
must allow only private RDS, MQ, S3 endpoint, TEI, Qdrant, Secrets Manager, KMS,
CloudWatch Logs, ECR and STS traffic. TEI and Qdrant must have no public ingress.
Planner, indexer, dispatcher, and cleanup each use a separate least-privilege
runtime secret and database identity, encrypted by the supplied customer-managed
KMS key. No function receives another function's dependency credentials.

The protected environment supplies an SNS-compatible alarm topic and the Amazon
MQ broker name. Lambda errors and throttles, plus either Retrieval DLQ receiving
a message, page the operator. Function log groups use the stack's configured
retention period.

Validate from the repository root:

```sh
make m5-mode-policy
make sam-m5-validate
```

Rollback disables Edge query routing, deploys `paused`, verifies zero active
preparation consumers, and preserves databases, queues, model cache, evidence,
vectors and encrypted Qdrant storage.
