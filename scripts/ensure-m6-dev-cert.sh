#!/usr/bin/env bash
set -euo pipefail
umask 077

dir="${1:-${CERT_DIR:-.dev/certs}}"
[[ -r "$dir/ca.crt" && -r "$dir/ca.key" ]] || { echo 'Development CA is required first' >&2; exit 1; }

generate_pair() {
  local name="$1"
  local cert="$dir/$name.crt"
  local key="$dir/$name.key"
  local csr="$dir/$name.csr"
  local extension="$dir/$name.ext"

  if [[ -r "$cert" && -r "$key" ]]; then
    return
  fi
  [[ ! -e "$cert" && ! -e "$key" ]] || { echo "Incomplete $name certificate pair; refusing overwrite" >&2; exit 1; }

  openssl genrsa -out "$key" 2048
  openssl req -new -key "$key" -subj "/CN=$name" -out "$csr"
  printf 'subjectAltName=DNS:%s,DNS:localhost\nextendedKeyUsage=serverAuth,clientAuth\n' "$name" > "$extension"
  openssl x509 -req -in "$csr" -CA "$dir/ca.crt" -CAkey "$dir/ca.key" -CAcreateserial \
    -out "$cert" -days 30 -sha256 -extfile "$extension"
  rm "$csr" "$extension"
  chmod 600 "$cert" "$key"
}

generate_pair answer-service
generate_pair llm-provider-stub
