#!/usr/bin/env bash
set -euo pipefail
umask 077

command -v openssl >/dev/null || { echo "openssl is required" >&2; exit 1; }

dir="${1:-.dev/certs}"
mkdir -p "$dir"
chmod 700 "$dir"
if compgen -G "$dir/*.key" >/dev/null; then
  echo "refusing to overwrite existing development keys in $dir" >&2
  exit 1
fi

openssl genrsa -out "$dir/ca.key" 4096
openssl req -x509 -new -nodes -key "$dir/ca.key" -sha256 -days 30 \
  -subj "/CN=raglibrarian-dev-ca" -out "$dir/ca.crt"

for service in edge-api identity-service catalog-service retrieval-service answer-service llm-provider-stub unknown-client; do
  openssl genrsa -out "$dir/$service.key" 2048
  openssl req -new -key "$dir/$service.key" -subj "/CN=$service" -out "$dir/$service.csr"
  printf 'subjectAltName=DNS:%s,DNS:localhost\nextendedKeyUsage=serverAuth,clientAuth\n' "$service" > "$dir/$service.ext"
  openssl x509 -req -in "$dir/$service.csr" -CA "$dir/ca.crt" -CAkey "$dir/ca.key" -CAcreateserial \
    -out "$dir/$service.crt" -days 30 -sha256 -extfile "$dir/$service.ext"
  rm "$dir/$service.csr" "$dir/$service.ext"
done

# Compose secrets expose service-specific copies inside containers. Keep every
# host-side credential source owner-readable only, including private keys.
chmod 600 "$dir"/*.crt "$dir"/*.key

echo "Generated local mTLS certificates in $dir"
