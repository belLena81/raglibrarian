#!/usr/bin/env bash
set -eu

image="${1:?image is required}"
scope="${2:?cache scope is required}"
shift 2

attempts="${IMAGE_BUILD_ATTEMPTS:-3}"
retry_base_seconds="${IMAGE_BUILD_RETRY_BASE_SECONDS:-5}"

case "$attempts" in
  ''|*[!0-9]*|0)
    echo "IMAGE_BUILD_ATTEMPTS must be a positive integer" >&2
    exit 2
    ;;
esac
case "$retry_base_seconds" in
  ''|*[!0-9]*|0)
    echo "IMAGE_BUILD_RETRY_BASE_SECONDS must be a positive integer" >&2
    exit 2
    ;;
esac

# Force base-10 arithmetic so valid values with leading zeroes remain valid.
attempts=$((10#$attempts))
retry_base_seconds=$((10#$retry_base_seconds))

attempt=1
while :; do
  if docker buildx build \
    --load \
    --cache-from "type=gha,scope=$scope" \
    --cache-to "type=gha,mode=max,scope=$scope" \
    "$@" \
    -t "$image" \
    .; then
    exit 0
  fi

  if [ "$attempt" -ge "$attempts" ]; then
    echo "Image build failed after $attempt attempt(s): $image" >&2
    exit 1
  fi

  delay=$((attempt * retry_base_seconds))
  echo "Image build attempt $attempt/$attempts failed for $image; retrying in ${delay}s..." >&2
  sleep "$delay"
  attempt=$((attempt + 1))
done
