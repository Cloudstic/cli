#!/usr/bin/env bash
#
# A MinIO backend for the benchmark scripts, and the request accounting that
# makes it worth measuring against.
#
# Peak RSS is the wrong headline for a remote store. What decides whether an
# operation is usable against S3 is how many requests it issues and how many
# bytes it moves: request latency, not bandwidth, sets the pace, and egress is
# billed. A change can leave memory untouched and still be the difference
# between a restore that takes a minute and one that takes an hour — the pack
# body cache being undersized (#458) cost 4.2 GB of re-reads while barely moving
# resident memory.
#
# MinIO is the local stand-in. It is not S3, and the numbers that transfer are
# the counts rather than the timings: a container on loopback has none of the
# latency that makes those counts matter, so treat requests and bytes as the
# measurement and wall time as a rough sanity check.
#
# Source this; it expects bash 3.2.

MINIO_IMAGE=${MINIO_IMAGE:-minio/minio:latest}
MINIO_CONTAINER=${MINIO_CONTAINER:-cloudstic-bench-minio}
MINIO_PORT=${MINIO_PORT:-19000}
MINIO_USER=${MINIO_USER:-cloudsticbench}
MINIO_PASSWORD=${MINIO_PASSWORD:-cloudsticbench}
MINIO_BUCKET=${MINIO_BUCKET:-bench}

# Every call to the container is bounded. A wedged MinIO would otherwise hang the
# sweep indefinitely, and a blank column is a far better outcome than a benchmark
# that never returns.
MINIO_CURL_TIMEOUT=${MINIO_CURL_TIMEOUT:-5}

minio_endpoint() { echo "http://127.0.0.1:$MINIO_PORT"; }

# minio_start launches the container and waits for it to answer.
#
# Prometheus auth is public so the metrics endpoint can be scraped without a
# signed request; the container is throwaway and bound to loopback.
minio_start() {
    minio_stop

    docker run -d --rm \
        --name "$MINIO_CONTAINER" \
        -p "127.0.0.1:$MINIO_PORT:9000" \
        -e "MINIO_ROOT_USER=$MINIO_USER" \
        -e "MINIO_ROOT_PASSWORD=$MINIO_PASSWORD" \
        -e "MINIO_PROMETHEUS_AUTH_TYPE=public" \
        "$MINIO_IMAGE" server /data >/dev/null || return 1

    local i
    for i in $(seq 1 60); do
        if curl -fs --max-time "$MINIO_CURL_TIMEOUT" "$(minio_endpoint)/minio/health/live" >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    echo "minio did not become ready on $(minio_endpoint)" >&2
    return 1
}

minio_stop() {
    docker rm -f "$MINIO_CONTAINER" >/dev/null 2>&1 || true
}

# minio_reset_bucket empties the bucket between samples, so each pipeline starts
# against an empty repository the way the local runs do.
minio_reset_bucket() {
    AWS_ACCESS_KEY_ID="$MINIO_USER" AWS_SECRET_ACCESS_KEY="$MINIO_PASSWORD" \
        aws --endpoint-url "$(minio_endpoint)" s3 rb "s3://$MINIO_BUCKET" --force >/dev/null 2>&1 || true
    AWS_ACCESS_KEY_ID="$MINIO_USER" AWS_SECRET_ACCESS_KEY="$MINIO_PASSWORD" \
        aws --endpoint-url "$(minio_endpoint)" s3 mb "s3://$MINIO_BUCKET" >/dev/null 2>&1
}

# minio_requests and minio_rx_bytes read counters out of the Prometheus endpoint.
#
# Both return an empty string when the metric is missing rather than failing.
# MinIO renames metrics between releases, and a benchmark that dies because a
# counter moved is worse than one reporting a blank column: the peak RSS and
# timing rows are still worth having.
# The v3 endpoint is the one carrying per-API counters; the v2 cluster endpoint
# reports storage and health but not request counts.
MINIO_API_METRICS="/minio/metrics/v3/api/requests"

minio_requests() {
    curl -fs --max-time "$MINIO_CURL_TIMEOUT" "$(minio_endpoint)$MINIO_API_METRICS" 2>/dev/null \
        | awk '/^minio_api_requests_total/ { s += $NF } END { printf "%d", s }'
}

minio_sent_bytes() {
    curl -fs --max-time "$MINIO_CURL_TIMEOUT" "$(minio_endpoint)$MINIO_API_METRICS" 2>/dev/null \
        | awk '/^minio_api_requests_traffic_sent_bytes/ { s += $NF } END { printf "%d", s }'
}

# minio_api_counts prints "NAME COUNT" per API, one per line.
#
# These are cumulative counters. Reporting them raw would put every earlier
# operation's requests in each row, so callers take two snapshots and subtract —
# see minio_api_delta.
minio_api_counts() {
    curl -fs --max-time "$MINIO_CURL_TIMEOUT" "$(minio_endpoint)$MINIO_API_METRICS" 2>/dev/null \
        | awk -F'"' '/^minio_api_requests_total/ {
              api = ""
              for (i = 1; i < NF; i++) if ($i ~ /name=$/) api = $(i + 1)
              if (api == "") next
              n = split($0, f, " ")
              printf "%s %d\n", api, f[n]
          }'
}

# minio_api_delta subtracts a "before" snapshot from an "after" one, printing
# only the APIs that moved, as "Name=N;Name=N".
#
# Written with awk over two files rather than an associative array: bash 3.2 is
# still what macOS ships and has none.
minio_api_delta() {
    awk '
        NR == FNR { before[$1] = $2; next }
        {
            d = $2 - (($1 in before) ? before[$1] : 0)
            if (d > 0) printf "%s%s=%d", (n++ ? ";" : ""), $1, d
        }
    ' "$1" "$2"
}

# minio_store_flags prints the flags that point cloudstic at the container.
#
# The store URI is "s3:<bucket>/<prefix>", matching what the hermetic e2e MinIO
# store passes — not "s3://", which the CLI does not accept.
minio_store_flags() {
    echo "-store s3:$MINIO_BUCKET/bench/ -s3-endpoint $(minio_endpoint) -s3-region us-east-1 -s3-access-key $MINIO_USER -s3-secret-key $MINIO_PASSWORD"
}
