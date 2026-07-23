# Azure serverless acceptance runner assets

These scripts deliberately contain no Azure credentials, RabbitMQ URIs, test
tokens, uploaded documents, prompts, or API keys. They are invoked by the
protected workflow on the private test runner.

`transition.sh` validates immutable image digests and versioned Key Vault URIs,
stops and waits for active job executions while pausing, deploys `paused` or
`serverless`, verifies the expected job execution ceilings, and invokes the
runner-owned broker consumer verifier. `run-acceptance.sh` delegates the
M4/M6/M7 corpus to a preprovisioned runner command and validates the sanitized
JSON evidence described in `ACCEPTANCE_CONTRACT.md`.

The scripts require Azure CLI with the Container Apps extension, Bicep, and
`jq`. They are not a substitute for application-level one-message adapters;
those commands must exist and be contract-tested before `serverless` is enabled.
