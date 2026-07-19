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

The artifact bucket and KMS key are retained on stack deletion. Their removal
is a separate audited data-retention operation, never part of rollback.
