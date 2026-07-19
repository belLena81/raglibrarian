#!/usr/bin/env sh
set -eu
root_user=$(cat /run/secrets/minio_root_user)
root_password=$(cat /run/secrets/minio_root_password)
access_key=$(cat /run/secrets/catalog_minio_access_key)
secret_key=$(cat /run/secrets/catalog_minio_secret_key)
ingestion_access_key=$(cat /run/secrets/ingestion_minio_access_key)
ingestion_secret_key=$(cat /run/secrets/ingestion_minio_secret_key)
cleanup_access_key=$(cat /run/secrets/ingestion_cleanup_minio_access_key)
cleanup_secret_key=$(cat /run/secrets/ingestion_cleanup_minio_secret_key)
mc alias set local http://minio:9000 "$root_user" "$root_password"
mc mb --ignore-existing local/original-books
mc anonymous set none local/original-books
mc mb --ignore-existing local/ingestion-artifacts
mc anonymous set none local/ingestion-artifacts
policy=$(mktemp)
trap 'rm -f "$policy"' EXIT
printf '%s' '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:PutObject","s3:GetObject","s3:DeleteObject","s3:AbortMultipartUpload","s3:ListMultipartUploadParts"],"Resource":["arn:aws:s3:::original-books/originals/*"]},{"Effect":"Allow","Action":["s3:ListBucket"],"Resource":["arn:aws:s3:::original-books"],"Condition":{"StringLike":{"s3:prefix":["originals/*"]}}},{"Effect":"Allow","Action":["s3:ListBucketMultipartUploads"],"Resource":["arn:aws:s3:::original-books"]}]}' > "$policy"
if mc admin user info local "$access_key" >/dev/null 2>&1; then
  mc admin policy detach local catalog-originals --user "$access_key" >/dev/null 2>&1 || true
fi
if mc admin policy info local catalog-originals >/dev/null 2>&1; then
  mc admin policy remove local catalog-originals
fi
mc admin policy create local catalog-originals "$policy"
if mc admin user info local "$access_key" >/dev/null 2>&1; then
  mc admin user remove local "$access_key"
fi
mc admin user add local "$access_key" "$secret_key"
mc admin policy attach local catalog-originals --user "$access_key"

printf '%s' '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::original-books/originals/*"]},{"Effect":"Allow","Action":["s3:ListBucket"],"Resource":["arn:aws:s3:::original-books"],"Condition":{"StringLike":{"s3:prefix":["originals/*"]}}},{"Effect":"Allow","Action":["s3:PutObject","s3:GetObject","s3:DeleteObject"],"Resource":["arn:aws:s3:::ingestion-artifacts/books/*"]},{"Effect":"Allow","Action":["s3:ListBucket"],"Resource":["arn:aws:s3:::ingestion-artifacts"],"Condition":{"StringLike":{"s3:prefix":["books/*"]}}}]}' > "$policy"
if mc admin user info local "$ingestion_access_key" >/dev/null 2>&1; then
  mc admin policy detach local ingestion-processing --user "$ingestion_access_key" >/dev/null 2>&1 || true
fi
if mc admin policy info local ingestion-processing >/dev/null 2>&1; then
  mc admin policy remove local ingestion-processing
fi
mc admin policy create local ingestion-processing "$policy"
if mc admin user info local "$ingestion_access_key" >/dev/null 2>&1; then
  mc admin user remove local "$ingestion_access_key"
fi
mc admin user add local "$ingestion_access_key" "$ingestion_secret_key"
mc admin policy attach local ingestion-processing --user "$ingestion_access_key"

printf '%s' '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:DeleteObject"],"Resource":["arn:aws:s3:::ingestion-artifacts/books/*"]},{"Effect":"Allow","Action":["s3:ListBucket"],"Resource":["arn:aws:s3:::ingestion-artifacts"],"Condition":{"StringLike":{"s3:prefix":["books/*"]}}}]}' > "$policy"
if mc admin user info local "$cleanup_access_key" >/dev/null 2>&1; then
  mc admin policy detach local ingestion-cleanup --user "$cleanup_access_key" >/dev/null 2>&1 || true
fi
if mc admin policy info local ingestion-cleanup >/dev/null 2>&1; then
  mc admin policy remove local ingestion-cleanup
fi
mc admin policy create local ingestion-cleanup "$policy"
if mc admin user info local "$cleanup_access_key" >/dev/null 2>&1; then
  mc admin user remove local "$cleanup_access_key"
fi
mc admin user add local "$cleanup_access_key" "$cleanup_secret_key"
mc admin policy attach local ingestion-cleanup --user "$cleanup_access_key"
