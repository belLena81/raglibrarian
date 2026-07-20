#!/usr/bin/env bash
set -euo pipefail
umask 077

dir="${1:-${CERT_DIR:-.dev/certs}}"
cert="$dir/retrieval-service.crt"
key="$dir/retrieval-service.key"
if [[ -r "$cert" && -r "$key" ]]; then
  exit 0
fi
[[ ! -e "$cert" && ! -e "$key" ]] || { echo 'Incomplete Retrieval certificate pair; refusing overwrite' >&2; exit 1; }
[[ -r "$dir/ca.crt" && -r "$dir/ca.key" ]] || { echo 'Development CA is required first' >&2; exit 1; }

openssl genrsa -out "$key" 2048
openssl req -new -key "$key" -subj '/CN=retrieval-service' -out "$dir/retrieval-service.csr"
printf 'subjectAltName=DNS:retrieval-service,DNS:localhost\nextendedKeyUsage=serverAuth,clientAuth\n' > "$dir/retrieval-service.ext"
openssl x509 -req -in "$dir/retrieval-service.csr" -CA "$dir/ca.crt" -CAkey "$dir/ca.key" -CAcreateserial \
  -out "$cert" -days 30 -sha256 -extfile "$dir/retrieval-service.ext"
rm "$dir/retrieval-service.csr" "$dir/retrieval-service.ext"
chmod 600 "$cert" "$key"
