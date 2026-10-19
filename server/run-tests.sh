#!/usr/bin/env bash
# Crowdblock performance test runner
#
# Usage:
#   ./run-tests.sh                    # Run all tests (default: 1min mixed)
#   ./run-tests.sh --quick            # Quick smoke test (15s)
#   ./run-tests.sh --load             # Load test (5min mixed)
#   ./run-tests.sh --read             # Read-only test (1min lists)
#   ./run-tests.sh --write            # Write-only test (1min reports)
#   ./run-tests.sh --cleanup          # Stop and remove containers
#
# Requires: docker, docker compose, k6 (or runs k6 via docker)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="${SCRIPT_DIR}/docker-compose-local.yaml"
PROJECT_NAME="crowdblock-test"

# Default test parameters
MODE="mixed"
DURATION="1m"
READ_RATE="100"
WRITE_RATE="50"
ACCOUNT_RATE="1"
USERS="100"
CLEANUP_AFTER=true

usage() {
    grep '^#' "$0" | cut -c4-
    exit 0
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --quick)
            MODE="mixed"
            DURATION="15s"
            READ_RATE="10"
            WRITE_RATE="5"
            USERS="10"
            ;;
        --load)
            MODE="mixed"
            DURATION="5m"
            READ_RATE="200"
            WRITE_RATE="100"
            ACCOUNT_RATE="2"
            USERS="200"
            ;;
        --read)
            MODE="read"
            DURATION="1m"
            READ_RATE="100"
            USERS="100"
            ;;
        --write)
            MODE="write"
            DURATION="1m"
            WRITE_RATE="50"
            USERS="20"
            ;;
        --cleanup)
            CLEANUP_ONLY=true
            ;;
        -h|--help)
            usage
            ;;
        *)
            echo "Unknown option: $1" >&2
            usage
            ;;
    esac
    shift
done

if [[ "${CLEANUP_ONLY:-false}" == "true" ]]; then
    echo "Cleaning up test environment..."
    docker compose -f "$COMPOSE_FILE" -p "$PROJECT_NAME" down -v --remove-orphans
    exit 0
fi

echo "=== Crowdblock Performance Test Runner ==="
echo "Mode: $MODE"
echo "Duration: $DURATION"
echo "Users: $USERS"
[[ -n "${READ_RATE:-}" ]] && echo "Read rate: $READ_RATE/s"
[[ -n "${WRITE_RATE:-}" ]] && echo "Write rate: $WRITE_RATE/s"
[[ -n "${ACCOUNT_RATE:-}" ]] && echo "Account rate: $ACCOUNT_RATE/s"
echo ""

# Check prerequisites
check_prereqs() {
    if ! command -v docker &> /dev/null; then
        echo "ERROR: docker not found" >&2
        exit 1
    fi
    if ! docker compose version &> /dev/null; then
        echo "ERROR: docker compose not found" >&2
        exit 1
    fi
    if ! command -v k6 &> /dev/null; then
        echo "WARNING: k6 not found locally, will run via docker"
        USE_DOCKER_K6=true
    else
        USE_DOCKER_K6=false
    fi
}

# Start the test stack
start_stack() {
    echo "Starting test stack..."
    docker compose -f "$COMPOSE_FILE" -p "$PROJECT_NAME" up -d --build

    echo "Waiting for services to be healthy..."
    local retries=60
    while [[ $retries -gt 0 ]]; do
        if docker compose -f "$COMPOSE_FILE" -p "$PROJECT_NAME" ps --format json | grep -q '"Health":"healthy"'; then
            local healthy_count=$(docker compose -f "$COMPOSE_FILE" -p "$PROJECT_NAME" ps --format json | grep -c '"Health":"healthy"' || true)
            if [[ $healthy_count -ge 3 ]]; then
                echo "All services healthy!"
                break
            fi
        fi
        sleep 2
        ((retries--))
    done

    if [[ $retries -eq 0 ]]; then
        echo "ERROR: Services did not become healthy in time" >&2
        docker compose -f "$COMPOSE_FILE" -p "$PROJECT_NAME" logs
        exit 1
    fi

    # Give server a moment to finish initialization
    sleep 3
}

# Run k6 test
run_k6() {
    local script="$1"
    shift
    local env_vars=(
        "BASE_URL=http://localhost:8080"
        "DURATION=$DURATION"
        "READ_RATE=$READ_RATE"
        "WRITE_RATE=$WRITE_RATE"
        "ACCOUNT_RATE=${ACCOUNT_RATE:-1}"
        "USERS=$USERS"
    )

    if [[ "$USE_DOCKER_K6" == "true" ]]; then
        echo "Running k6 via docker..."
        docker run --rm --network host \
            -v "$SCRIPT_DIR/k6:/k6:ro" \
            -e BASE_URL="$BASE_URL" \
            -e DURATION="$DURATION" \
            -e READ_RATE="$READ_RATE" \
            -e WRITE_RATE="$WRITE_RATE" \
            -e ACCOUNT_RATE="${ACCOUNT_RATE:-1}" \
            -e USERS="$USERS" \
            grafana/k6 run "/k6/$script" "$@"
    else
        echo "Running k6 locally..."
        BASE_URL="http://localhost:8080" \
        DURATION="$DURATION" \
        READ_RATE="$READ_RATE" \
        WRITE_RATE="$WRITE_RATE" \
        ACCOUNT_RATE="${ACCOUNT_RATE:-1}" \
        USERS="$USERS" \
        k6 run "$SCRIPT_DIR/k6/$script" "$@"
    fi
}

# Run selected test scenario
run_tests() {
    case "$MODE" in
        mixed)
            echo "=== Running Mixed Load Test ==="
            run_k6 "mixed.js"
            ;;
        read)
            echo "=== Running Read-Only Test ==="
            run_k6 "lists.js"
            ;;
        write)
            echo "=== Running Write-Only Test ==="
            run_k6 "reports.js"
            ;;
        *)
            echo "Unknown mode: $MODE" >&2
            exit 1
            ;;
    esac
}

# Cleanup
cleanup() {
    if [[ "$CLEANUP_AFTER" == "true" ]]; then
        echo ""
        echo "Cleaning up..."
        docker compose -f "$COMPOSE_FILE" -p "$PROJECT_NAME" down -v --remove-orphans
    fi
}

trap cleanup EXIT

main() {
    check_prereqs
    start_stack
    run_tests
    echo ""
    echo "=== Test completed successfully ==="
}

main "$@"