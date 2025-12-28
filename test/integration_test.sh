#!/bin/bash
# Integration tests for aqsh API
# Usage: ./test/integration_test.sh
#
# Prerequisites:
#   - docker compose up -d (or run aqsh with redis)
#   - aqsh server running on localhost:8080

set -e

BASE_URL="${AQSH_URL:-http://localhost:8080}"
PASS=0
FAIL=0

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

pass() {
    echo -e "${GREEN}✓ PASS${NC}: $1" >&2
    PASS=$((PASS + 1))
}

fail() {
    echo -e "${RED}✗ FAIL${NC}: $1" >&2
    echo "  Expected: $2" >&2
    echo "  Got: $3" >&2
    FAIL=$((FAIL + 1))
}

info() {
    echo -e "${YELLOW}→${NC} $1" >&2
}

# Wait for server to be ready
wait_for_server() {
    info "Waiting for server at $BASE_URL..."
    for i in {1..30}; do
        if curl -s "$BASE_URL/health" > /dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    echo "Server not ready after 30 seconds"
    exit 1
}

# Test: GET /health
test_health() {
    info "Testing GET /health"
    RESP=$(curl -s "$BASE_URL/health")

    if echo "$RESP" | grep -q '"status":"healthy"'; then
        pass "GET /health returns healthy status"
    else
        fail "GET /health returns healthy status" '{"status":"healthy",...}' "$RESP"
    fi

    if echo "$RESP" | grep -q '"redis":"connected"'; then
        pass "GET /health shows redis connected"
    else
        fail "GET /health shows redis connected" '"redis":"connected"' "$RESP"
    fi
}

# Test: GET /metrics (asynq queue metrics)
test_metrics() {
    info "Testing GET /metrics"
    RESP=$(curl -s "$BASE_URL/metrics")

    if echo "$RESP" | grep -q 'asynq_queue_size'; then
        pass "GET /metrics returns asynq_queue_size"
    else
        fail "GET /metrics returns asynq_queue_size" "asynq_queue_size" "$RESP"
    fi

    if echo "$RESP" | grep -q 'asynq_tasks_enqueued_total'; then
        pass "GET /metrics returns asynq_tasks_enqueued_total"
    else
        fail "GET /metrics returns asynq_tasks_enqueued_total" "asynq_tasks_enqueued_total" "$RESP"
    fi
}

# Test: GET /metrics (after jobs have run)
test_metrics_after_jobs() {
    info "Testing GET /metrics (after jobs)"
    RESP=$(curl -s "$BASE_URL/metrics")

    if echo "$RESP" | grep -q 'asynq_tasks_processed_total'; then
        pass "GET /metrics returns asynq_tasks_processed_total"
    else
        fail "GET /metrics returns asynq_tasks_processed_total" "asynq_tasks_processed_total" "$RESP"
    fi

    if echo "$RESP" | grep -q 'asynq_queue_latency_seconds'; then
        pass "GET /metrics returns asynq_queue_latency_seconds"
    else
        fail "GET /metrics returns asynq_queue_latency_seconds" "asynq_queue_latency_seconds" "$RESP"
    fi
}

# Test: GET /hooks
test_list_hooks() {
    info "Testing GET /hooks"
    RESP=$(curl -s "$BASE_URL/hooks")

    if echo "$RESP" | grep -q '"hooks"'; then
        pass "GET /hooks returns hooks object"
    else
        fail "GET /hooks returns hooks object" '{"hooks":{...}}' "$RESP"
    fi

    if echo "$RESP" | grep -q '"hello"'; then
        pass "Hooks list contains 'hello'"
    else
        fail "Hooks list contains 'hello'" "hello in list" "$RESP"
    fi

    if echo "$RESP" | grep -q '"deploy"'; then
        pass "Hooks list contains 'deploy'"
    else
        fail "Hooks list contains 'deploy'" "deploy in list" "$RESP"
    fi
}

# Test: POST /jobs/{hook} - success
test_submit_job_success() {
    info "Testing POST /jobs/hello (success case)"
    RESP=$(curl -s -X POST -H "Content-Type: application/json" \
        -d '{"name":"IntegrationTest"}' \
        "$BASE_URL/jobs/hello")

    if echo "$RESP" | grep -q '"id"'; then
        JOB_ID=$(echo "$RESP" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
        pass "POST /jobs/hello returns job ID: $JOB_ID"
        echo "$JOB_ID"
    else
        fail "POST /jobs/hello returns job ID" '{"id":"..."}' "$RESP"
        echo ""
    fi
}

# Test: POST /jobs/{hook} - validation error (missing required field)
test_submit_job_validation_error() {
    info "Testing POST /jobs/hello (missing required field)"
    RESP=$(curl -s -X POST -H "Content-Type: application/json" \
        -d '{}' \
        "$BASE_URL/jobs/hello")
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        -H "Content-Type: application/json" -d '{}' \
        "$BASE_URL/jobs/hello")

    if [ "$HTTP_CODE" = "400" ]; then
        pass "POST /jobs/hello with missing field returns 400"
    else
        fail "POST /jobs/hello with missing field returns 400" "400" "$HTTP_CODE"
    fi

    if echo "$RESP" | grep -q '"error"'; then
        pass "POST /jobs/hello with missing field returns error message"
    else
        fail "POST /jobs/hello with missing field returns error message" '{"error":"..."}' "$RESP"
    fi
}

# Test: POST /jobs/{hook} - validation error (invalid pattern)
test_submit_job_invalid_pattern() {
    info "Testing POST /jobs/deploy (invalid version pattern)"
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        -H "Content-Type: application/json" \
        -d '{"version":"invalid","environment":"prod"}' \
        "$BASE_URL/jobs/deploy")

    if [ "$HTTP_CODE" = "400" ]; then
        pass "POST /jobs/deploy with invalid version returns 400"
    else
        fail "POST /jobs/deploy with invalid version returns 400" "400" "$HTTP_CODE"
    fi
}

# Test: POST /jobs/{hook} - validation error (invalid enum)
test_submit_job_invalid_enum() {
    info "Testing POST /jobs/deploy (invalid environment enum)"
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        -H "Content-Type: application/json" \
        -d '{"version":"1.0.0","environment":"invalid"}' \
        "$BASE_URL/jobs/deploy")

    if [ "$HTTP_CODE" = "400" ]; then
        pass "POST /jobs/deploy with invalid environment returns 400"
    else
        fail "POST /jobs/deploy with invalid environment returns 400" "400" "$HTTP_CODE"
    fi
}

# Test: POST /jobs/{hook} - unknown field
test_submit_job_unknown_field() {
    info "Testing POST /jobs/hello (unknown field)"
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        -H "Content-Type: application/json" \
        -d '{"name":"test","unknown":"field"}' \
        "$BASE_URL/jobs/hello")

    if [ "$HTTP_CODE" = "400" ]; then
        pass "POST /jobs/hello with unknown field returns 400"
    else
        fail "POST /jobs/hello with unknown field returns 400" "400" "$HTTP_CODE"
    fi
}

# Test: POST /jobs/{hook} - unknown hook
test_submit_unknown_hook() {
    info "Testing POST /jobs/nonexistent (404)"
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        -H "Content-Type: application/json" -d '{}' \
        "$BASE_URL/jobs/nonexistent")

    if [ "$HTTP_CODE" = "404" ]; then
        pass "POST /jobs/nonexistent returns 404"
    else
        fail "POST /jobs/nonexistent returns 404" "404" "$HTTP_CODE"
    fi
}

# Test: GET /jobs/{id} - success result
test_get_job_success() {
    local JOB_ID=$1
    info "Testing GET /jobs/$JOB_ID (completed job)"

    # Wait for job to complete
    sleep 5

    RESP=$(curl -s "$BASE_URL/jobs/$JOB_ID")

    if echo "$RESP" | grep -q '"status":"completed"'; then
        pass "GET /jobs/$JOB_ID shows status=completed"
    else
        fail "GET /jobs/$JOB_ID shows status=completed" "completed" "$RESP"
    fi

    if echo "$RESP" | grep -q '"exit_code":0'; then
        pass "GET /jobs/$JOB_ID shows exit_code=0"
    else
        fail "GET /jobs/$JOB_ID shows exit_code=0" "exit_code:0" "$RESP"
    fi

    if echo "$RESP" | grep -q 'Hello, IntegrationTest'; then
        pass "GET /jobs/$JOB_ID output contains greeting"
    else
        fail "GET /jobs/$JOB_ID output contains greeting" "Hello, IntegrationTest" "$RESP"
    fi
}

# Test: GET /jobs/{id} - not found
test_get_job_not_found() {
    info "Testing GET /jobs/nonexistent-id (not found)"
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/jobs/nonexistent-id")

    if [ "$HTTP_CODE" = "404" ]; then
        pass "GET /jobs/nonexistent-id returns 404"
    else
        fail "GET /jobs/nonexistent-id returns 404" "404" "$HTTP_CODE"
    fi
}

# Test: GET /jobs/{id}/logs - log streaming
test_get_job_logs() {
    local JOB_ID=$1
    info "Testing GET /jobs/$JOB_ID/logs (log streaming)"

    RESP=$(curl -s "$BASE_URL/jobs/$JOB_ID/logs")

    if echo "$RESP" | grep -q 'Hello, IntegrationTest'; then
        pass "GET /jobs/$JOB_ID/logs contains output"
    else
        fail "GET /jobs/$JOB_ID/logs contains output" "Hello, IntegrationTest" "$RESP"
    fi

    if echo "$RESP" | grep -q 'event: eof'; then
        pass "GET /jobs/$JOB_ID/logs ends with EOF event"
    else
        fail "GET /jobs/$JOB_ID/logs ends with EOF event" "event: eof" "$RESP"
    fi
}

# Test: Real-time log streaming for long-running job
test_realtime_log_streaming() {
    info "Testing real-time log streaming with slow job"

    # Submit a slow job (takes ~5 seconds)
    RESP=$(curl -s -X POST -H "Content-Type: application/json" \
        -d '{}' \
        "$BASE_URL/jobs/slow")

    JOB_ID=$(echo "$RESP" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
    if [ -z "$JOB_ID" ]; then
        fail "Submit slow job" "job ID" "$RESP"
        return
    fi
    pass "Submitted slow job: $JOB_ID"

    # Start streaming logs immediately (job is still running)
    # Use timeout to not wait forever, capture partial output
    sleep 1  # Give job a moment to start

    # Stream logs for 3 seconds while job is still running
    LOGS=$(timeout 3 curl -s -N "$BASE_URL/jobs/$JOB_ID/logs" 2>/dev/null || true)

    # Check that we got some intermediate output (not just EOF)
    if echo "$LOGS" | grep -q 'Step 1 of 5'; then
        pass "Real-time streaming shows step 1"
    else
        fail "Real-time streaming shows step 1" "Step 1 of 5" "$LOGS"
    fi

    # Wait for job to complete
    sleep 5

    # Now get complete logs
    LOGS=$(curl -s "$BASE_URL/jobs/$JOB_ID/logs")

    if echo "$LOGS" | grep -q 'Step 5 of 5'; then
        pass "Complete logs contain final step"
    else
        fail "Complete logs contain final step" "Step 5 of 5" "$LOGS"
    fi

    if echo "$LOGS" | grep -q 'Slow job completed!'; then
        pass "Complete logs contain completion message"
    else
        fail "Complete logs contain completion message" "Slow job completed!" "$LOGS"
    fi

    if echo "$LOGS" | grep -q 'event: eof'; then
        pass "Log stream ends with EOF"
    else
        fail "Log stream ends with EOF" "event: eof" "$LOGS"
    fi
}

# Test: Deploy job with valid parameters
test_deploy_job() {
    info "Testing POST /jobs/deploy (valid parameters)"
    RESP=$(curl -s -X POST -H "Content-Type: application/json" \
        -d '{"version":"1.2.3","environment":"prod"}' \
        "$BASE_URL/jobs/deploy")

    if echo "$RESP" | grep -q '"id"'; then
        JOB_ID=$(echo "$RESP" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
        pass "POST /jobs/deploy returns job ID: $JOB_ID"

        # Wait and check result
        sleep 5
        RESP=$(curl -s "$BASE_URL/jobs/$JOB_ID")

        if echo "$RESP" | grep -q '"status":"completed"'; then
            pass "Deploy job completed successfully"
        else
            fail "Deploy job completed successfully" "completed" "$RESP"
        fi

        if echo "$RESP" | grep -q 'Deploying version 1.2.3 to prod'; then
            pass "Deploy job output contains correct version and environment"
        else
            fail "Deploy job output contains correct version and environment" "Deploying version 1.2.3 to prod" "$RESP"
        fi
    else
        fail "POST /jobs/deploy returns job ID" '{"id":"..."}' "$RESP"
    fi
}

# Main
echo "========================================" >&2
echo "aqsh Integration Tests" >&2
echo "========================================" >&2
echo "" >&2

wait_for_server

echo "" >&2
echo "--- Health & Hooks Tests ---" >&2
test_health
test_metrics
test_list_hooks

echo "" >&2
echo "--- Validation Tests ---" >&2
test_submit_unknown_hook
test_submit_job_validation_error
test_submit_job_invalid_pattern
test_submit_job_invalid_enum
test_submit_job_unknown_field
test_get_job_not_found

echo "" >&2
echo "--- Job Execution Tests ---" >&2
SUCCESS_JOB_ID=$(test_submit_job_success)

if [ -n "$SUCCESS_JOB_ID" ]; then
    test_get_job_success "$SUCCESS_JOB_ID"
    test_get_job_logs "$SUCCESS_JOB_ID"
fi

test_deploy_job

echo "" >&2
echo "--- Log Streaming Tests ---" >&2
test_realtime_log_streaming

echo "" >&2
echo "--- Metrics Tests (after jobs) ---" >&2
test_metrics_after_jobs

echo "" >&2
echo "========================================" >&2
echo -e "Results: ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC}" >&2
echo "========================================" >&2

if [ $FAIL -gt 0 ]; then
    exit 1
fi
