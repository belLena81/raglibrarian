# M4 AWS deployment

This SAM stack owns the PDF-processing Lambdas, the optional Fargate worker,
CloudWatch alarms, and the encrypted artifact bucket. It consumes shared VPC,
Amazon MQ, PostgreSQL, source-bucket, KMS-key, and Secrets Manager resources as
parameters. All image parameters must be immutable ECR digest references.

`DatabaseSecretArn` must contain exactly one string field named `dsn` for the
processing/dispatcher database role. `CleanupDatabaseSecretArn` must contain
the same field for a distinct `ingestion_cleanup` login, which has only SELECT
and the cleanup-state column updates granted by the M4 migration. Both AWS
DSNs must use `sslmode=verify-full`. The publisher secret must contain exactly
one string field named `uri` using `amqps`. The separate Amazon MQ event-source
secret contains the AWS-required `username` and `password` fields. Do not put
any secret values in parameter files, shell history, CloudFormation outputs,
or CI logs.

The artifact bucket is versioned. Cleanup roles can list versions and delete
object versions only below `books/*`; cleanup is complete only after all object
versions and delete markers under the deterministic artifact prefix are gone.

## Private-network prerequisites

The supplied private subnets and security groups must reach PostgreSQL and the
RabbitMQ broker without public ingress. They must also have private endpoints
or controlled NAT egress for Secrets Manager, CloudWatch Logs, STS, Lambda,
ECR API/ECR Docker, and KMS. Add an S3 gateway endpoint scoped to the source
and artifact buckets. The on-demand Amazon MQ event-source path also requires
the Lambda, STS, and Secrets Manager interface endpoints when NAT is absent.
DNS resolution and private DNS must be enabled. Runtime security-group egress
should be limited to those dependencies and endpoints.

Validate before deployment:

```sh
make sam-validate
```

M4 is not complete merely because local and CI gates pass. The reviewed image
digests must also complete `.github/workflows/m4-staging.yml` in the protected
`m4-staging` environment. That manual workflow exercises and verifies the
exclusive `paused -> lambda -> paused -> worker -> paused` handoff. The gate
always leaves consumers paused before deleting its isolated broker vhost. A
failed, cancelled, or unapproved run is a release blocker.

The protected environment must require reviewers and restrict deployments to
the intended release branch. It supplies:

- secret `AWS_ROLE_ARN`, an OIDC-assumable least-privilege deployment role;
- secret `M4_STAGING_PARAMETER_OVERRIDES_JSON`, a JSON object containing the
  environment-specific SAM parameters but no processing mode or image values;
- secret `M4_STAGING_E2E_SECRET_ARN`, the ARN of one Secrets Manager JSON
  object containing exactly the private test fields listed below;
- protected variables `AWS_ACCOUNT_ID`, `AWS_REGION`, and `M4_STACK_NAME`; and
- protected variable `M4_SAM_ARTIFACT_BUCKET`, the explicit staging package
  bucket used before every deployment sequence.

The OIDC role needs the bounded CloudFormation, SAM artifact bucket, ECR image
inspection, ECS/Lambda verification, and CloudWatch alarm permissions used by
the workflow. Its `secretsmanager:GetSecretValue` statement must name only the
single ARN in `M4_STAGING_E2E_SECRET_ARN`; it must not wildcard staging or
production secrets.

The E2E secret contains exactly these string fields:

- `lambda_access_refresh_token` and `lambda_revocable_refresh_token`;
- `recovery_access_refresh_token`;
- `worker_access_refresh_token` and `worker_revocable_refresh_token`;
- `ingestion_postgres_dsn`, `rabbitmq_uri`, `minio_access_key`, and
  `minio_secret_key`;
- `rabbitmq_vhost`, exactly `raglibrarian-m4-<github-run-id>`, plus a bounded
  one-use `rabbitmq_cleanup_url` ending in `/vhosts/<that-name>` and its
  `rabbitmq_cleanup_token`;
- `edge_base_urls`, exactly two distinct comma-separated pathless HTTPS
  origins, and one pathless HTTPS `public_origin`;
- pathless HTTPS `minio_endpoint` and valid S3 `minio_artifact_bucket`.

Endpoint and bucket destinations live in this role-protected exact-key JSON,
not mutable workflow variables. The workflow bounds them, rejects CR/LF,
userinfo, paths, queries, and fragments, and only then extracts them for test
use.

Each refresh token belongs to a distinct disposable active reader session.
The workflow rotates each token once through private Edge immediately before
its adapter phase and writes only the resulting short-lived access token to an
owner-only file. Lambda and worker must never share the revocable session,
because the full SSE contract deliberately revokes it. Provision a staging-only
RabbitMQ identity and isolated per-run virtual host whose name is bound to the
GitHub run ID and contains the expected exchange and DLQ names. The protected
cleanup endpoint must authorize deletion of only that run-bound vhost; the
workflow calls it on success and from its failure trap, and teardown failure
fails the gate. Delete the disposable broker identity with the vhost. That
identity may write only to `raglibrarian.events.v1`, read only
`ingestion.book-uploaded.dlq.v1`, and configure nothing. The local persistent
`ingestion_e2e` credential is a Compose fixture, not a production or staging
credential. Database and object credentials are read-only staging test roles.

All five refresh tokens are single-use and rotate when consumed. Replace the
complete E2E secret with five fresh disposable sessions before every workflow
attempt, including a retry after cancellation or failure; a partial run may
already have consumed any subset of them.

The private runner must carry the labels `self-hosted`, `linux`, `x64`, and
`m4-staging-private`, use a current GitHub runner with Node 24 action support,
and provide Go 1.26.5, AWS CLI 2.35.19, SAM CLI 1.145.2, and `jq` 1.7.1. It
must not be shared with untrusted pull requests. It must have private network
reachability to both Edge instances, PostgreSQL, RabbitMQ, and the object
endpoint. The workflow writes parameter and E2E material only below a
mode-0700 temporary directory with mode-0600 files, emits only sanitized gate
results, and removes the files on every exit.

Active processing hosts must support Landlock filesystem restrictions. The
processing image runs `/parser-sandbox --landlock-preflight` during bootstrap;
Lambda or Fargate environments that do not expose the required Landlock kernel
support fail deployment/runtime validation before consuming PDF work. Do not
replace this with unsandboxed Poppler execution.

Only these four ECR repositories are trusted in the protected account and
region: `raglibrarian/m4-processing`, `raglibrarian/m4-dispatcher`,
`raglibrarian/m4-cleanup`, and `raglibrarian/m4-worker`. Every input must be a
digest URI in its matching repository. Before deployment, the workflow reads
the image manifest and config through ECR APIs and requires OCI label
`org.opencontainers.image.revision` to equal the reviewed `GITHUB_SHA`.

The workflow runs the synthetic contract corpus, poison/DLQ and live-status
checks, and bounded `m4-slo-v1` performance smoke in Lambda mode. It then
pauses all consumers, accepts one upload, enables worker mode, and proves that
the queued document reaches one deterministic durable result before running
the same contract and performance gates in worker mode. Any failure triggers a
best-effort deployment back to `paused`; operators must still verify the
consumer and alarm state before retrying.

The alarm gate requires exactly the five unconditional alarm resources while
paused and those five plus the two active-consumer alarms in Lambda or worker
mode. Every expected metric alarm must report `OK`; `ALARM`,
`INSUFFICIENT_DATA`, a missing alarm, or an unexpected extra alarm blocks the
run and cannot be reported as clear.

Package validation is non-mutating except for uploading deployment artifacts
to the explicitly supplied packaging bucket:

```sh
M4_SAM_ARTIFACT_BUCKET='<deployment-artifact-bucket>' make sam-package-check
```

## Safe consumer-mode handoff

`ProcessingMode` defaults to `paused`. Never switch directly between `lambda`
and `worker`: a CloudFormation update can briefly overlap old and new
consumers. First deploy `paused`, wait for the Lambda event-source mapping to
be disabled and the ECS service to reach zero running tasks, then deploy the
new mode. Use deployment automation with a protected parameter file; the
commands below intentionally omit environment-specific parameter values.

```sh
aws cloudformation deploy --template-file infra/aws/m4/template.yaml --stack-name "$M4_STACK" --parameter-overrides ProcessingMode=paused
aws lambda list-event-source-mappings --function-name "$M4_PROCESSING_FUNCTION"
aws ecs describe-services --cluster "$M4_WORKER_CLUSTER" --services "$M4_WORKER_SERVICE"
aws cloudformation deploy --template-file infra/aws/m4/template.yaml --stack-name "$M4_STACK" --parameter-overrides ProcessingMode=lambda
```

For worker fallback, use the same pause-and-verify sequence and set the final
mode to `worker`. Roll back by deploying `paused`, verifying consumers are
stopped, and re-enabling the last known-good image digest in its prior mode.
Processing Lambda changes use the `live` alias and the configured canary; an
error alarm causes CodeDeploy rollback. Dispatcher, cleanup, queue-depth,
consumer-count, and dead-letter alarms require operator notification routing.

The protected workflow's sanitized step summary is the release evidence. Keep
the workflow run URL, reviewed commit, immutable image digests, approver, final
mode, and change ticket in the deployment record. Never copy parameter JSON,
credential output, application tokens, uploaded documents, or raw CloudWatch
logs into that record.

The artifact bucket and KMS key are retained on stack deletion. Their removal
is a separate audited data-retention operation, never part of rollback.
