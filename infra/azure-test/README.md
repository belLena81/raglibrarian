# Azure private serverless test adapters

This deployment replaces only the asynchronous M4/M5 consumers on the private
test host with Azure Container Apps Jobs. Edge, Identity, Catalog, Answer,
PostgreSQL, Qdrant, TEI, RabbitMQ, and object storage remain on the private VM.
It is a protected acceptance environment, not a production topology.

## Safety contract

The only valid handoff is `paused -> worker -> paused -> serverless -> paused`.
Never run portable workers and Container Apps Jobs at the same time. The
protected workflow omits event jobs while `paused`, verifies the broker's
consumer count is zero, then enables event jobs in `serverless`. It always
attempts to return jobs to `paused` on completion, cancellation, or failure.

Each KEDA RabbitMQ rule uses `QueueLength`, a target of one message, and one
replica completion per execution. Ingestion is capped at two executions; each
retrieval planner/index queue at four. A one-shot adapter must ACK only after
the shared durable operation has succeeded, preserve duplicate-safe behavior,
and leave cancelled deliveries unsettled for broker recovery.

## Prerequisites

- Create a dedicated VNet subnet delegated for Container Apps infrastructure;
  the environment is internal only and has no ingress. It must resolve and
  route exclusively to the test VM's private endpoints.
- Create a private ACR and push reviewed images using immutable `@sha256`
  references. The template creates one identity per job and grants each only
  `AcrPull` on that registry.
- Create a private Key Vault. The template derives each job identity's exact
  secret-name scope from its fixed secret mappings and assigns `Key Vault
  Secrets User` at those individual secret resources. The caller supplies only
  the 32-hex `secretVersions` values; Bicep constructs every URI using the
  declared vault and fixed logical secret name, so a deployment cannot widen a
  job's Key Vault scope.
- Use a dedicated RabbitMQ vhost and account for this acceptance environment.
  The URI must be AMQPS and is supplied to KEDA from Key Vault. Do not reuse
  shared CloudAMQP credentials: KEDA needs a credential that can inspect the
  relevant queues, while application adapters should retain least privilege.
- Keep the parameter file and every runner command outside this repository's
  tracked files. The example is shape-only and contains no valid identities,
  endpoints, images, or credential values.

The template creates the environment, one managed identity, and five
event-driven jobs: M4 ingestion; M5 planner for uploaded, chunks-ready, and
lifecycle events; and M5 index. It also creates four one-shot scheduled jobs
only while `serverless` is enabled: M4/M5 dispatchers every minute and M4/M5
cleanup every 15 minutes. Pausing deletes those schedules and event jobs.

Build each event image with its service's `cmd/serverless_job` as the image
entrypoint. The four retrieval event job definitions inject
`RETRIEVAL_SERVERLESS_QUEUE` with their exact source queue. Dispatcher and
cleanup images must likewise use bounded one-shot entrypoints; never point a
schedule at a long-lived worker binary.

## Deployment and protected workflow

Copy the example parameter file to an owner-only location outside the checkout,
replace every shape-only value with protected environment references, and
validate it:

```sh
deploy/azure-test/scripts/validate-deployment.sh /secure/raglibrarian-azure.json
```

The GitHub environment `azure-serverless-test` must require approval and use a
private runner with network access to the VM, RabbitMQ management endpoint, and
Azure control plane. Configure these protected variables without secret values
in workflow output:

| Variable | Purpose |
| --- | --- |
| `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID` | OIDC deployment identity coordinates |
| `AZURE_TEST_RESOURCE_GROUP` | Dedicated test resource group |
| `AZURE_TEST_PARAMETERS_FILE` | Owner-only parameter file on the private runner |
| `AZURE_VERIFY_CONSUMERS_COMMAND` | Preprovisioned command accepting `zero` and checking RabbitMQ consumers |
| `AZURE_SET_WORKER_MODE_COMMAND` | Preprovisioned command accepting `worker` or `paused` on the VM |
| `AZURE_ACCEPTANCE_COMMAND` | Sanitized M4/M6/M7 release corpus command |
| `AZURE_ACR_LOGIN_SERVER` | Exact approved private ACR login host |
| `AZURE_ALLOWED_IMAGE_REPOSITORIES` | Comma-separated trusted repository paths |
| `AZURE_VERIFY_APPROVED_IMAGE_COMMAND` | Runner command that verifies the reviewed digest and OCI revision label |

The deployment identity may create/update only the named Container Apps
environment/jobs, their identities/role assignments, and read job state. It
must not read Key Vault secret values, access VM SSH credentials, or have broad
subscription permissions. The job identities may pull reviewed ACR images and
resolve only their individually scoped Key Vault secret references.
