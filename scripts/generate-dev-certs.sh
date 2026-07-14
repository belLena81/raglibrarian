#!/usr/bin/env bash
set -euo pipefail

dir="${1:-.dev/certs}"
mkdir -p "$dir"

openssl genrsa -out "$dir/ca.key" 4096
openssl req -x509 -new -nodes -key "$dir/ca.key" -sha256 -days 30 \
  -subj "/CN=raglibrarian-dev-ca" -out "$dir/ca.crt"

for service in edge-api identity-service catalog-service; do
  openssl genrsa -out "$dir/$service.key" 2048
  openssl req -new -key "$dir/$service.key" -subj "/CN=$service" -out "$dir/$service.csr"
  printf 'subjectAltName=DNS:%s,DNS:localhost\nextendedKeyUsage=serverAuth,clientAuth\n' "$service" > "$dir/$service.ext"
  openssl x509 -req -in "$dir/$service.csr" -CA "$dir/ca.crt" -CAkey "$dir/ca.key" -CAcreateserial \
    -out "$dir/$service.crt" -days 30 -sha256 -extfile "$dir/$service.ext"
  rm "$dir/$service.csr" "$dir/$service.ext"
done

echo "Generated local mTLS certificates in $dir"
