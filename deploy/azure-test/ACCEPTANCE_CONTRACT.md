# Protected acceptance evidence contract

The private runner command receives `m4 m6 m7` arguments and must write one JSON
document to `RAGLIBRARIAN_ACCEPTANCE_EVIDENCE_FILE`. The workflow validates this
file and prints only a one-line sanitized summary.

Required shape:

```json
{
  "mode": "worker",
  "commit": "0123456789abcdef0123456789abcdef01234567",
  "milestones": ["m4", "m6", "m7"],
  "sanitized": true,
  "secret_leakage": false,
  "forbidden_payload_leakage": false,
  "m4": { "status": "passed" },
  "m6": { "status": "passed" },
  "m7": { "status": "passed" }
}
```

`mode` is `worker` for the private worker-host stage and `serverless` for the
Azure Container Apps Jobs stage. `commit` must match the expected workflow or
checkout commit supplied to the evidence validator.

The evidence must not include credentials, connection strings, bearer tokens,
raw uploaded documents, retrieved passages, full prompts, provider responses, or
raw logs. Store only sanitized result IDs, counts, queue names, timings, commit
and image digest references, and pass/fail status.
