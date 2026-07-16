#!/usr/bin/env sh
set -eu
root_user=$(cat /run/secrets/minio_root_user)
root_password=$(cat /run/secrets/minio_root_password)
access_key=$(cat /run/secrets/catalog_minio_access_key)
secret_key=$(cat /run/secrets/catalog_minio_secret_key)
mc alias set local http://minio:9000 "$root_user" "$root_password"
mc mb --ignore-existing local/original-books
mc anonymous set none local/original-books
policy=$(mktemp)
trap 'rm -f "$policy"' EXIT
printf '%s' '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:PutObject","s3:GetObject","s3:DeleteObject"],"Resource":["arn:aws:s3:::original-books/originals/*"]},{"Effect":"Allow","Action":["s3:ListBucket"],"Resource":["arn:aws:s3:::original-books"],"Condition":{"StringLike":{"s3:prefix":["originals/*"]}}}]}' > "$policy"
if mc admin policy info local catalog-originals >/dev/null 2>&1; then
  mc admin policy remove local catalog-originals
fi
mc admin policy create local catalog-originals "$policy"
if mc admin user info local "$access_key" >/dev/null 2>&1; then
  mc admin user remove local "$access_key"
fi
mc admin user add local "$access_key" "$secret_key"
mc admin policy attach local catalog-originals --user "$access_key"
