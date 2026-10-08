package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/hibiken/asynq"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rophy/aqsh/internal/tasks"
	"github.com/rophy/aqsh/internal/worker"
)

type mcpTask struct {
	taskName string
	tool     *mcp.Tool
	schema   *jsonschema.Resolved
}

func resolveSchema(schema any) (*jsonschema.Resolved, error) {
	data, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var parsed jsonschema.Schema
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	return parsed.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
}

func (s *Server) mcpHandler() (http.Handler, error) {
	if err := s.tasks.ValidateMCP(); err != nil {
		return nil, err
	}
	catalog := make([]mcpTask, 0)
	for name, definition := range s.tasks.Tasks {
		if definition.MCP == nil || !definition.MCP.Enabled {
			continue
		}
		task, err := s.tasks.Resolve(name)
		if err != nil {
			return nil, err
		}
		schema, err := s.tasks.MCPInputSchema(name)
		if err != nil {
			return nil, err
		}
		resolved, err := resolveSchema(schema)
		if err != nil {
			return nil, fmt.Errorf("task %q MCP schema: %w", name, err)
		}
		description := task.MCP.Description
		if description == "" {
			description = task.Description
		}
		description += " Submits a background execution; use aqsh_get_execution to poll its status."
		destructive := false
		catalog = append(catalog, mcpTask{
			taskName: name, schema: resolved,
			tool: &mcp.Tool{
				Name: task.MCP.Name, Description: description, InputSchema: schema,
				OutputSchema: map[string]any{"type": "object"},
				Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &destructive},
			},
		})
	}
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].tool.Name < catalog[j].tool.Name })

	executionSchema := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"execution_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}},
		"required":   []string{"execution_id"},
	}
	executionResolved, err := resolveSchema(executionSchema)
	if err != nil {
		return nil, err
	}
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		p := s.requestPrincipal(r)
		server := mcp.NewServer(&mcp.Implementation{Name: "aqsh", Version: s.version}, nil)
		// A separate stateless view prevents catalogs and sessions leaking across
		// principals. Schemas are compiled once, before serving requests.
		for _, entry := range catalog {
			task, err := s.tasks.Resolve(entry.taskName)
			if err != nil || !exposedTask(task, entry.tool.Name) || !canUseTask(p, task) {
				continue
			}
			server.AddTool(entry.tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				task, err := s.authorizeTask(entry.taskName, p)
				if err != nil {
					return mcpFailure(err), nil
				}
				if !exposedTask(task, entry.tool.Name) {
					return mcpFailure(failure(http.StatusNotFound, "tool not available")), nil
				}
				payload, err := toolArguments(req, entry.schema)
				if err != nil {
					return mcpFailure(err), nil
				}
				result, err := s.submit(ctx, entry.taskName, p, payload)
				if err != nil {
					return mcpFailure(err), nil
				}
				return mcpResult(map[string]any{
					"execution_id": result.ID, "status": result.Status, "poll_after_ms": 1000,
				}), nil
			})
		}
		server.AddTool(&mcp.Tool{
			Name:        "aqsh_get_execution",
			Description: "Get the status and approved JSON result of your own execution of an exposed task. A completed execution may still report an unhealthy target.",
			InputSchema: executionSchema, OutputSchema: map[string]any{"type": "object"},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args, err := toolArguments(req, executionResolved)
			if err != nil {
				return mcpFailure(err), nil
			}
			id := args["execution_id"].(string)
			info, task, err := s.readExecution(ctx, id, p, true)
			if err != nil {
				slog.InfoContext(ctx, "MCP execution access denied", "identity", p.Identity, "execution_id", id)
				return mcpFailure(err), nil
			}
			slog.InfoContext(ctx, "MCP execution read", "identity", p.Identity, "execution_id", id)
			return mcpResult(s.mcpExecutionResult(ctx, info, task)), nil
		})
		return server
	}, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 1 << 20,
		PropagateRequestCancellation: true,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origins := r.Header.Values("Origin")
		if len(origins) > 1 || len(origins) == 1 && !slices.Contains(s.cfg.MCPAllowedOrigins, origins[0]) {
			s.jsonError(w, http.StatusForbidden, "origin not allowed")
			return
		}
		if s.requestPrincipal(r).Identity == "" {
			s.jsonError(w, http.StatusUnauthorized, "identity header required")
			return
		}
		handler.ServeHTTP(w, r)
	}), nil
}

func exposedTask(task *tasks.ResolvedTask, toolName string) bool {
	return task.MCP != nil && task.MCP.Enabled && task.MCP.ReadOnly && task.MCP.Name == toolName
}

func toolArguments(req *mcp.CallToolRequest, schema *jsonschema.Resolved) (map[string]any, error) {
	data := req.Params.Arguments
	if len(data) == 0 {
		data = json.RawMessage("{}")
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil || payload == nil {
		return nil, failure(http.StatusBadRequest, "tool arguments must be a JSON object")
	}
	if err := schema.Validate(payload); err != nil {
		return nil, failure(http.StatusBadRequest, "invalid tool arguments")
	}
	return payload, nil
}

func mcpResult(value map[string]any) *mcp.CallToolResult {
	data, _ := json.Marshal(value)
	return &mcp.CallToolResult{
		StructuredContent: value, Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}
}

func mcpFailure(err error) *mcp.CallToolResult {
	status, message := http.StatusInternalServerError, "internal error"
	var e *serviceError
	if errors.As(err, &e) {
		status, message = e.Status, e.Message
	}
	result := mcpResult(map[string]any{"error": map[string]any{"code": status, "message": message}})
	result.IsError = true
	return result
}

func (s *Server) mcpExecutionResult(ctx context.Context, info *asynq.TaskInfo, task *tasks.ResolvedTask) map[string]any {
	result := map[string]any{
		"execution_id": info.ID, "status": stateToStatus(info.State),
		"retried": info.Retried, "max_retry": info.MaxRetry,
	}
	var payload worker.TaskPayload
	if json.Unmarshal(info.Payload, &payload) == nil && !payload.CreatedAt.IsZero() {
		result["created_at"] = payload.CreatedAt.Format(time.RFC3339)
	}
	if ms, err := s.rdb.HGet(ctx, worker.MetaKeyPrefix+info.ID, "started_at").Int64(); err == nil {
		result["started_at"] = time.UnixMilli(ms).Format(time.RFC3339)
	}
	if info.State != asynq.TaskStateCompleted && info.State != asynq.TaskStateArchived {
		result["poll_after_ms"] = 1000
		return result
	}
	if !info.CompletedAt.IsZero() {
		result["completed_at"] = info.CompletedAt.Format(time.RFC3339)
	}
	if task.MCP.ResultMode != "json" {
		result["result_omitted"] = "not_enabled"
		return result
	}
	var outcome worker.TaskResult
	if len(info.Result) == 0 || json.Unmarshal(info.Result, &outcome) != nil {
		result["result_omitted"] = "unavailable"
		return result
	}
	result["exit_code"] = outcome.ExitCode
	switch {
	case outcome.Data == nil:
		result["result_omitted"] = "absent"
	case len(*outcome.Data) > task.MCP.ResultLimit():
		result["result_omitted"] = "too_large"
	case len(*outcome.Data) == 0:
		result["result_omitted"] = "empty"
	default:
		var data any
		if json.Unmarshal([]byte(*outcome.Data), &data) != nil {
			result["result_omitted"] = "invalid_json"
		} else {
			result["data"] = data
		}
	}
	return result
}
