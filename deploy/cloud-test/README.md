# Cloud-neutral managed-services test deployment

This profile runs the RAGLibrarian frontend, backend services, portable workers,
PostgreSQL, Qdrant, and real TEI embeddings on one Linux VM. Its application
contracts are cloud-neutral: S3-compatible object storage, AMQPS RabbitMQ,
STARTTLS SMTP, and an OpenAI-compatible HTTPS LLM. The free defaults are
Cloudflare R2, CloudAMQP, Brevo, and Groq; Tailscale Serve is the only HTTPS
entry point.

The same Compose files run on a VM from AWS, Azure, Google Cloud,
DigitalOcean, OCI, Hetzner, or another provider. Only `.env.cloud` and the
provider secret files change:

| Capability | Free-test default | Production-compatible alternatives |
|---|---|---|
| Compute/front/back | One Linux VM + Docker Compose | EC2, Azure VM, Compute Engine, Droplet, OCI Compute, Kubernetes |
| Background execution | Portable ingestion/retrieval workers | Existing AWS Lambda adapters, containers, Cloud Run jobs, Azure Container Apps jobs |
| Object storage | Cloudflare R2 | AWS S3, DigitalOcean Spaces, private MinIO; every provider must pass the live contract |
| Email | Brevo SMTP | Amazon SES SMTP, SendGrid, Mailgun, another authenticated STARTTLS relay |
| RabbitMQ | CloudAMQP | Amazon MQ for RabbitMQ, dedicated CloudAMQP, self-hosted RabbitMQ |
| PostgreSQL | Local persistent container | RDS, Azure Database for PostgreSQL, Cloud SQL, DigitalOcean Managed PostgreSQL |
| LLM | Groq open-model API | Any compatible HTTPS provider or a self-hosted gateway |

The portable workers execute the same application logic as the AWS Lambda
adapters and are intentionally used for the cross-cloud test. Running actual
Lambda would make the environment AWS-specific and would add VPC, NAT or
endpoints, Amazon MQ, RDS, KMS, Secrets Manager, and image-registry costs.

It is a private functional-test deployment, not a production topology. The
CloudAMQP free shared plan has one credential and vhost, so this profile uses
classic durable queues and cannot enforce the repository's normal per-service
broker permissions or quorum delivery semantics.

The topology converter removes quorum delivery limits only from the six
primary queues whose consumers enforce application retry bounds: five attempts
for Ingestion and Catalog, and four for Retrieval. An active retry-publication
failure dead-letters the original delivery early instead of requeueing it with
an unchanged attempt count; during shutdown, an unsettled delivery is recovered
when the broker connection closes. Confirmed retry publication followed by ACK
is still at-least-once and has a crash window that can produce duplicates, so
handlers must remain idempotent. Classic dead-lettering also has weaker delivery
guarantees than quorum at-least-once dead-lettering. Finally, the shared test
credential can forge the application attempt header and force early DLQ
delivery; use only authorized test data here, and use separate least-privilege
broker identities and quorum queues in production.

After the host is started, the release-candidate worker stage is run through:

```sh
bash deploy/cloud-test/scripts/run-worker-acceptance.sh
```

Set `RAGLIBRARIAN_WORKER_ACCEPTANCE_COMMAND` to the private runner command that
mints disposable sessions, exercises the M4/M6/M7 corpus, and writes sanitized
JSON to `RAGLIBRARIAN_ACCEPTANCE_EVIDENCE_FILE`.

## 1. Provision providers

Create the following resources before touching the host:

- A Linux VM with Ubuntu, 2 OCPUs, 12 GiB RAM, and at
  least a 100 GiB boot volume. Allow outbound HTTPS, but do not open application
  ports in the cloud firewall.
- A CloudAMQP Little Lemur **RabbitMQ** instance. Copy its complete `amqps://`
  URL exactly; it contains percent-encoded credentials and the assigned vhost.
- Two private S3-compatible buckets: one for original books and one for
  ingestion artifacts. Cloudflare R2 EU is the default. Do not enable public
  access or browser CORS.
- Three S3 credential pairs: Catalog read/write on originals, Ingestion read
  on originals plus read/write/list/delete on artifacts, and Retrieval read on
  artifacts.
- A STARTTLS SMTP account. Brevo is the free default; Amazon SES SMTP is a
  drop-in production alternative. The current adapter requires port 587 or
  2525 and does not implement implicit SMTPS on port 465.
- A Groq API key. Enable Zero Data Retention in Groq Data Controls before any
  private book or passage is sent to answer mode.
- A Tailscale node enrolled in the intended private tailnet.

Provider free tiers and cloud capacity can change. Configure provider usage or
billing alerts, and verify the current limits before each test period.

For a RabbitMQ provider whose management API is not on the AMQPS hostname, set
`RABBITMQ_MANAGEMENT_BASE_URL` to its trusted HTTPS `/api` endpoint. The
topology tool authenticates there with the username and password from
`rabbitmq_uri`; never point this setting at an unrelated or untrusted host.

The example environment pins the ARM64 TEI image for the cheapest Ampere VM.
For an x86_64 VM, set `M5_TEI_IMAGE` to the repository's reviewed amd64 digest
before rendering Compose. Never run a foreign-architecture model image through
emulation in this small test environment.

## 2. Prepare the host

Install Docker Engine with Compose v2.24.4 or newer, Tailscale, Go, OpenSSL,
`jq`, and the Hugging Face `hf` CLI. Clone the repository on the VM,
then run from the repository root:

```sh
cp deploy/cloud-test/.env.cloud.example deploy/cloud-test/.env.cloud
$EDITOR deploy/cloud-test/.env.cloud
bash deploy/cloud-test/scripts/prepare_runtime.sh
```

The preparation command prints the one-time administrator bootstrap code. Save
it outside the repository and do not place it in shell history, logs, or CI.

Download the pinned embedding model when instructed:

```sh
M5_MODEL_DIR="$PWD/deploy/cloud-test/runtime/models/m5-jina-code-v1" \
  bash scripts/bootstrap-m5-model.sh
```

## 3. Install provider secrets

Create a temporary owner-only directory outside the repository containing
exactly these files, each mode `0400` or `0600`:

```text
rabbitmq_uri
smtp_password
groq_api_key
s3_catalog_access_key
s3_catalog_secret_key
s3_ingestion_access_key
s3_ingestion_secret_key
s3_retrieval_access_key
s3_retrieval_secret_key
```

Each file contains only the corresponding value and an optional final newline.
For R2, the S3 values must be access-key credentials, not Cloudflare account
API tokens. Install them without exposing values in process arguments:

```sh
bash deploy/cloud-test/scripts/install_provider_secrets.sh /secure/provider-files
```

Securely delete the temporary source directory after verifying the installed
files. Runtime credentials stay under the ignored
`deploy/cloud-test/runtime/secrets` directory.

## 4. Run compatibility gates

Validate the merged Compose model and the test-only topology conversion:

```sh
bash deploy/cloud-test/scripts/validate.sh
```

Before uploading private data, run the repository's exact Catalog object-store
adapter against the selected S3-compatible service. This verifies streaming
upload, CRC32C receipt, stat, and delete behavior rather than assuming generic
S3 compatibility:

```sh
docker compose \
  --env-file deploy/cloud-test/.env.cloud \
  -f docker-compose.yml \
  -f deploy/cloud-test/compose.yaml \
  --profile cloud-storage-test \
  run --rm catalog-minio-runtime-tests
```

Treat any skip or failure as a deployment blocker. Do not weaken Catalog's
integrity check to make a provider pass.

## 5. Start privately

```sh
bash deploy/cloud-test/scripts/start.sh
```

The start command imports exchanges, classic queues, retry queues, DLQs, and
bindings through the RabbitMQ management API, starts the stack, and configures
Tailscale Serve to forward private HTTPS traffic to the loopback-only web
container. Re-running it is safe.

Open the `PUBLIC_ORIGIN`, use the saved one-time code at `/setup/admin`, then
disable the verifier path in `.env.cloud` and recreate Identity so bootstrap
cannot be reused. Keep the file only as an offline recovery artifact:

```sh
# Set IDENTITY_BOOTSTRAP_VERIFIER_FILE= in deploy/cloud-test/.env.cloud first.
docker compose \
  --env-file deploy/cloud-test/.env.cloud \
  -f docker-compose.yml \
  -f deploy/cloud-test/compose.yaml \
  --profile m5 --profile m6 \
  up -d --force-recreate identity-service
```

## Acceptance and operations

- Confirm `/api/healthz` and `/api/readyz` through the Tailscale URL and verify no VM
  port is reachable from the public Internet.
- Register a controlled recipient and verify confirmation and reset mail through
  the selected SMTP relay.
- Upload an authorized test PDF and observe queued, chunks-ready, and indexed
  states; verify CloudAMQP retry and DLQ behavior with a controlled dependency
  interruption.
- Query in evidence-only mode first. Enable answer mode only after confirming
  Groq Zero Data Retention, then verify every answer segment cites returned
  evidence.
- Confirm provider URIs, keys, book text, retrieved passages, email tokens, and
  prompts never appear in container logs.
- Delete the test book and confirm removal from both object buckets and Qdrant.
- Back up PostgreSQL before host maintenance. This free single-node deployment
  has no high availability and may be reclaimed when idle.

CloudAMQP free shared queues can be removed after long inactivity. Re-run the
start command to restore topology before resuming a test. For production, use a
dedicated broker with separate identities and quorum queues, a durable managed
database, authenticated domain email, and reviewed backup/restore procedures.
