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

# Test: GET /tasks (list task types)
test_list_tasks() {
    info "Testing GET /tasks"
    RESP=$(curl -s "$BASE_URL/tasks")

    if echo "$RESP" | grep -q '"tasks"'; then
        pass "GET /tasks returns tasks object"
    else
        fail "GET /tasks returns tasks object" '{"tasks":{...}}' "$RESP"
    fi

    if echo "$RESP" | grep -q '"hello"'; then
        pass "Tasks list contains 'hello'"
    else
        fail "Tasks list contains 'hello'" "hello in list" "$RESP"
    fi

    if echo "$RESP" | grep -q '"deploy"'; then
        pass "Tasks list contains 'deploy'"
    else
        fail "Tasks list contains 'deploy'" "deploy in list" "$RESP"
    fi
}

# Test: POST /tasks/{name} - success
test_submit_task_success() {
    info "Testing POST /tasks/hello (success case)"
    RESP=$(curl -s -X POST -H "Content-Type: application/json" \
        -d '{"name":"IntegrationTest"}' \
        "$BASE_URL/tasks/hello")

    if echo "$RESP" | grep -q '"id"'; then
        TASK_ID=$(echo "$RESP" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
        pass "POST /tasks/hello returns task ID: $TASK_ID"
        echo "$TASK_ID"
    else
        fail "POST /tasks/hello returns task ID" '{"id":"..."}' "$RESP"
        echo ""
    fi
}

# Test: POST /tasks/{name} - validation error (missing required field)
test_submit_task_validation_error() {
    info "Testing POST /tasks/hello (missing required field)"
    RESP=$(curl -s -X POST -H "Content-Type: application/json" \
        -d '{}' \
        "$BASE_URL/tasks/hello")
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        -H "Content-Type: application/json" -d '{}' \
        "$BASE_URL/tasks/hello")

    if [ "$HTTP_CODE" = "400" ]; then
        pass "POST /tasks/hello with missing field returns 400"
    else
        fail "POST /tasks/hello with missing field returns 400" "400" "$HTTP_CODE"
    fi

    if echo "$RESP" | grep -q '"error"'; then
        pass "POST /tasks/hello with missing field returns error message"
    else
        fail "POST /tasks/hello with missing field returns error message" '{"error":"..."}' "$RESP"
    fi
}

# Test: POST /tasks/{name} - validation error (invalid pattern)
test_submit_task_invalid_pattern() {
    info "Testing POST /tasks/deploy (invalid version pattern)"
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        -H "Content-Type: application/json" \
        -d '{"version":"invalid","environment":"prod"}' \
        "$BASE_URL/tasks/deploy")

    if [ "$HTTP_CODE" = "400" ]; then
        pass "POST /tasks/deploy with invalid version returns 400"
    else
        fail "POST /tasks/deploy with invalid version returns 400" "400" "$HTTP_CODE"
    fi
}

# Test: POST /tasks/{name} - validation error (invalid enum)
test_submit_task_invalid_enum() {
    info "Testing POST /tasks/deploy (invalid environment enum)"
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        -H "Content-Type: application/json" \
        -d '{"version":"1.0.0","environment":"invalid"}' \
        "$BASE_URL/tasks/deploy")

    if [ "$HTTP_CODE" = "400" ]; then
        pass "POST /tasks/deploy with invalid environment returns 400"
    else
        fail "POST /tasks/deploy with invalid environment returns 400" "400" "$HTTP_CODE"
    fi
}

# Test: POST /tasks/{name} - unknown field
test_submit_task_unknown_field() {
    info "Testing POST /tasks/hello (unknown field)"
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        -H "Content-Type: application/json" \
        -d '{"name":"test","unknown":"field"}' \
        "$BASE_URL/tasks/hello")

    if [ "$HTTP_CODE" = "400" ]; then
        pass "POST /tasks/hello with unknown field returns 400"
    else
        fail "POST /tasks/hello with unknown field returns 400" "400" "$HTTP_CODE"
    fi
}

# Test: POST /tasks/{name} - unknown task
test_submit_unknown_task() {
    info "Testing POST /tasks/nonexistent (404)"
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        -H "Content-Type: application/json" -d '{}' \
        "$BASE_URL/tasks/nonexistent")

    if [ "$HTTP_CODE" = "404" ]; then
        pass "POST /tasks/nonexistent returns 404"
    else
        fail "POST /tasks/nonexistent returns 404" "404" "$HTTP_CODE"
    fi
}

# Test: GET /tasks/{id} - success result
test_get_task_success() {
    local TASK_ID=$1
    info "Testing GET /tasks/$TASK_ID (completed task)"

    # Wait for task to complete
    sleep 5

    RESP=$(curl -s "$BASE_URL/tasks/$TASK_ID")

    if echo "$RESP" | grep -q '"status":"completed"'; then
        pass "GET /tasks/$TASK_ID shows status=completed"
    else
        fail "GET /tasks/$TASK_ID shows status=completed" "completed" "$RESP"
    fi

    if echo "$RESP" | grep -q '"exit_code":0'; then
        pass "GET /tasks/$TASK_ID shows exit_code=0"
    else
        fail "GET /tasks/$TASK_ID shows exit_code=0" "exit_code:0" "$RESP"
    fi

    # Check for created_at and started_at timestamps
    if echo "$RESP" | grep -q '"created_at"'; then
        pass "GET /tasks/$TASK_ID shows created_at"
    else
        fail "GET /tasks/$TASK_ID shows created_at" '"created_at":"..."' "$RESP"
    fi

    if echo "$RESP" | grep -q '"started_at"'; then
        pass "GET /tasks/$TASK_ID shows started_at"
    else
        fail "GET /tasks/$TASK_ID shows started_at" '"started_at":"..."' "$RESP"
    fi

    # Check for result data from AQSH_RESULT_FILE (stored as string)
    if echo "$RESP" | grep -q '"data":"'; then
        pass "GET /tasks/$TASK_ID result contains data string"
    else
        fail "GET /tasks/$TASK_ID result contains data string" '"data":"..."' "$RESP"
    fi

    # The data string should contain the greeted field (escaped JSON)
    if echo "$RESP" | grep -q 'greeted'; then
        pass "GET /tasks/$TASK_ID result data contains greeted field"
    else
        fail "GET /tasks/$TASK_ID result data contains greeted field" 'greeted' "$RESP"
    fi
}

# Test: GET /tasks/{id} - not found
test_get_task_not_found() {
    info "Testing GET /tasks/nonexistent-id (not found)"
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/tasks/nonexistent-id")

    if [ "$HTTP_CODE" = "404" ]; then
        pass "GET /tasks/nonexistent-id returns 404"
    else
        fail "GET /tasks/nonexistent-id returns 404" "404" "$HTTP_CODE"
    fi
}

# Test: GET /tasks/{id}/logs - log streaming
test_get_task_logs() {
    local TASK_ID=$1
    info "Testing GET /tasks/$TASK_ID/logs (log streaming)"

    RESP=$(curl -s "$BASE_URL/tasks/$TASK_ID/logs")

    if echo "$RESP" | grep -q 'Hello, IntegrationTest'; then
        pass "GET /tasks/$TASK_ID/logs contains output"
    else
        fail "GET /tasks/$TASK_ID/logs contains output" "Hello, IntegrationTest" "$RESP"
    fi

    if echo "$RESP" | grep -q 'event: eof'; then
        pass "GET /tasks/$TASK_ID/logs ends with EOF event"
    else
        fail "GET /tasks/$TASK_ID/logs ends with EOF event" "event: eof" "$RESP"
    fi
}

# Test: Real-time log streaming for long-running task
test_realtime_log_streaming() {
    info "Testing real-time log streaming with slow task"

    # Submit a slow task (takes ~5 seconds)
    RESP=$(curl -s -X POST -H "Content-Type: application/json" \
        -d '{}' \
        "$BASE_URL/tasks/slow")

    TASK_ID=$(echo "$RESP" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
    if [ -z "$TASK_ID" ]; then
        fail "Submit slow task" "task ID" "$RESP"
        return
    fi
    pass "Submitted slow task: $TASK_ID"

    # Start streaming logs immediately (task is still running)
    # Use timeout to not wait forever, capture partial output
    sleep 1  # Give task a moment to start

    # Stream logs for 3 seconds while task is still running
    LOGS=$(timeout 3 curl -s -N "$BASE_URL/tasks/$TASK_ID/logs" 2>/dev/null || true)

    # Check that we got some intermediate output (not just EOF)
    if echo "$LOGS" | grep -q 'Step 1 of 5'; then
        pass "Real-time streaming shows step 1"
    else
        fail "Real-time streaming shows step 1" "Step 1 of 5" "$LOGS"
    fi

    # Wait for task to complete
    sleep 5

    # Now get complete logs
    LOGS=$(curl -s "$BASE_URL/tasks/$TASK_ID/logs")

    if echo "$LOGS" | grep -q 'Step 5 of 5'; then
        pass "Complete logs contain final step"
    else
        fail "Complete logs contain final step" "Step 5 of 5" "$LOGS"
    fi

    if echo "$LOGS" | grep -q 'Slow task completed!'; then
        pass "Complete logs contain completion message"
    else
        fail "Complete logs contain completion message" "Slow task completed!" "$LOGS"
    fi

    if echo "$LOGS" | grep -q 'event: eof'; then
        pass "Log stream ends with EOF"
    else
        fail "Log stream ends with EOF" "event: eof" "$LOGS"
    fi
}

# Test: Deploy task with valid parameters
test_deploy_task() {
    info "Testing POST /tasks/deploy (valid parameters)"
    RESP=$(curl -s -X POST -H "Content-Type: application/json" \
        -d '{"version":"1.2.3","environment":"prod"}' \
        "$BASE_URL/tasks/deploy")

    if echo "$RESP" | grep -q '"id"'; then
        TASK_ID=$(echo "$RESP" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
        pass "POST /tasks/deploy returns task ID: $TASK_ID"

        # Wait and check result
        sleep 5
        RESP=$(curl -s "$BASE_URL/tasks/$TASK_ID")

        if echo "$RESP" | grep -q '"status":"completed"'; then
            pass "Deploy task completed successfully"
        else
            fail "Deploy task completed successfully" "completed" "$RESP"
        fi

        # Check for result data from AQSH_RESULT_FILE (stored as string, search for unescaped content)
        if echo "$RESP" | grep -q 'deployed'; then
            pass "Deploy task result data contains status=deployed"
        else
            fail "Deploy task result data contains status=deployed" 'deployed' "$RESP"
        fi

        if echo "$RESP" | grep -q '1.2.3'; then
            pass "Deploy task result data contains correct version"
        else
            fail "Deploy task result data contains correct version" '1.2.3' "$RESP"
        fi
    else
        fail "POST /tasks/deploy returns task ID" '{"id":"..."}' "$RESP"
    fi
}

# Test: Deploy task with dry_run
test_deploy_task_dry_run() {
    info "Testing POST /tasks/deploy (dry run)"
    RESP=$(curl -s -X POST -H "Content-Type: application/json" \
        -d '{"version":"2.0.0","environment":"staging","dry_run":true}' \
        "$BASE_URL/tasks/deploy")

    if echo "$RESP" | grep -q '"id"'; then
        TASK_ID=$(echo "$RESP" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
        pass "POST /tasks/deploy (dry_run) returns task ID: $TASK_ID"

        # Wait and check result
        sleep 3
        RESP=$(curl -s "$BASE_URL/tasks/$TASK_ID")

        if echo "$RESP" | grep -q '"status":"completed"'; then
            pass "Deploy dry_run task completed successfully"
        else
            fail "Deploy dry_run task completed successfully" "completed" "$RESP"
        fi

        # Check for dry_run status in result (stored as string)
        if echo "$RESP" | grep -q 'dry_run'; then
            pass "Deploy dry_run result data contains status=dry_run"
        else
            fail "Deploy dry_run result data contains status=dry_run" 'dry_run' "$RESP"
        fi
    else
        fail "POST /tasks/deploy (dry_run) returns task ID" '{"id":"..."}' "$RESP"
    fi
}

# Test: Task without result file (data field should be omitted)
test_task_no_result_file() {
    info "Testing task without AQSH_RESULT_FILE (slow task)"
    RESP=$(curl -s -X POST -H "Content-Type: application/json" \
        -d '{}' \
        "$BASE_URL/tasks/slow")

    if echo "$RESP" | grep -q '"id"'; then
        TASK_ID=$(echo "$RESP" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
        pass "POST /tasks/slow returns task ID: $TASK_ID"

        # Wait for task to complete
        sleep 7
        RESP=$(curl -s "$BASE_URL/tasks/$TASK_ID")

        if echo "$RESP" | grep -q '"status":"completed"'; then
            pass "Slow task completed successfully"
        else
            fail "Slow task completed successfully" "completed" "$RESP"
        fi

        # Verify data field is NOT present (script didn't write to result file)
        if echo "$RESP" | grep -q '"data"'; then
            fail "Result should NOT contain data field" "no data field" "$RESP"
        else
            pass "Result correctly omits data field (no result file written)"
        fi
    else
        fail "POST /tasks/slow returns task ID" '{"id":"..."}' "$RESP"
    fi
}

# Main
echo "========================================" >&2
echo "aqsh Integration Tests" >&2
echo "========================================" >&2
echo "" >&2

wait_for_server

echo "" >&2
echo "--- Health & Tasks List Tests ---" >&2
test_health
test_metrics
test_list_tasks

echo "" >&2
echo "--- Validation Tests ---" >&2
test_submit_unknown_task
test_submit_task_validation_error
test_submit_task_invalid_pattern
test_submit_task_invalid_enum
test_submit_task_unknown_field
test_get_task_not_found

echo "" >&2
echo "--- Task Execution Tests ---" >&2
SUCCESS_TASK_ID=$(test_submit_task_success)

if [ -n "$SUCCESS_TASK_ID" ]; then
    test_get_task_success "$SUCCESS_TASK_ID"
    test_get_task_logs "$SUCCESS_TASK_ID"
fi

test_deploy_task
test_deploy_task_dry_run
test_task_no_result_file

echo "" >&2
echo "--- Log Streaming Tests ---" >&2
test_realtime_log_streaming

echo "" >&2
echo "--- Metrics Tests (after tasks) ---" >&2
test_metrics_after_jobs

echo "" >&2
echo "========================================" >&2
echo -e "Results: ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC}" >&2
echo "========================================" >&2

if [ $FAIL -gt 0 ]; then
    exit 1
fi
