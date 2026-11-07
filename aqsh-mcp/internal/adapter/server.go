package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

type entry struct {
	config     ToolConfig
	definition taskDef
	tool       *mcp.Tool
	schema     *jsonschema.Resolved
}

func New(ctx context.Context, cfg Config) (http.Handler, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 10 * time.Second,
		Transport:     &http.Transport{Proxy: nil, ResponseHeaderTimeout: 5 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	b := &backend{cfg.BackendURL, client}
	if err := b.check(ctx); err != nil {
		return nil, err
	}
	verifier, err := newVerifier(ctx, cfg, client)
	if err != nil {
		return nil, err
	}
	// Catch unsupported schemas before listening. Actual catalogs are fetched with
	// each caller's identity, allowing current ACLs and values_url policy to apply.
	for _, t := range cfg.Tools {
		d, err := b.definition(ctx, t.Task, principal{})
		if err != nil {
			return nil, fmt.Errorf("load task %q: %w", t.Task, err)
		}
		if _, _, err := inputSchema(d.Inputs); err != nil {
			return nil, fmt.Errorf("task %q: %w", t.Task, err)
		}
	}
	pollSchema := map[string]any{"type": "object", "additionalProperties": false,
		"properties": map[string]any{"execution_id": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,256}$"}},
		"required":   []string{"execution_id"}}
	pollResolved, err := resolveSchema(pollSchema)
	if err != nil {
		return nil, err
	}
	type viewKey struct{}
	stream := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		p := requestPrincipal(r)
		entries := r.Context().Value(viewKey{}).([]entry)
		server := mcp.NewServer(&mcp.Implementation{Name: "aqsh-mcp", Version: "0.1.0-dev"}, nil)
		for _, e := range entries {
			server.AddTool(e.tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				args, err := arguments(req.Params.Arguments, e.definition.Inputs, e.schema)
				if err != nil {
					return toolError(err), nil
				}
				var submitted struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				}
				if err := b.request(ctx, "POST", "/tasks/"+url.PathEscape(e.config.Task), p, args, &submitted); err != nil {
					return toolError(err), nil
				}
				if submitted.ID == "" {
					return toolError(&apiError{502}), nil
				}
				return toolResult(map[string]any{"execution_id": submitted.ID, "status": submitted.Status, "poll_after_ms": 1000}), nil
			})
		}
		server.AddTool(&mcp.Tool{Name: "aqsh_get_execution",
			Description: "Get your own execution of an exposed task. Poll until completed or failed; completion does not imply a healthy target.",
			InputSchema: pollSchema, OutputSchema: map[string]any{"type": "object"},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args, err := arguments(req.Params.Arguments, nil, pollResolved)
			if err != nil {
				return toolError(err), nil
			}
			var result execution
			id := args["execution_id"].(string)
			if err := b.request(ctx, "GET", "/executions/"+url.PathEscape(id)+"?include_result_json=true", p, nil, &result); err != nil {
				return toolError(err), nil
			}
			if result.Identity != p.identity || result.ID != id {
				return toolError(&apiError{404}), nil
			}
			for _, e := range entries {
				if e.config.Task == result.Task {
					return toolResult(result.output(e.config)), nil
				}
			}
			return toolError(&apiError{404}), nil
		})
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 1 << 20, PropagateRequestCancellation: true})
	view := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := b.check(r.Context()); err != nil {
			http.Error(w, "backend unavailable or unprotected", 503)
			return
		}
		p := requestPrincipal(r)
		entries := []entry{}
		for _, t := range cfg.Tools {
			d, err := b.definition(r.Context(), t.Task, p)
			if err != nil {
				http.Error(w, "task catalog unavailable", 503)
				return
			}
			if !d.allowed(p) {
				continue
			}
			schema, resolved, err := inputSchema(d.Inputs)
			if err != nil {
				http.Error(w, "task schema unavailable", 503)
				return
			}
			description := t.Description
			if description == "" {
				description = d.Description
			}
			description += " Submits a background execution; use aqsh_get_execution to poll."
			destructive := false
			entries = append(entries, entry{t, d, &mcp.Tool{Name: t.Name, Description: description, InputSchema: schema,
				OutputSchema: map[string]any{"type": "object"}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &destructive}}, resolved})
		}
		stream.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), viewKey{}, entries)))
	})
	public, _ := url.Parse(cfg.PublicURL)
	metadataURL := public.Scheme + "://" + public.Host + "/.well-known/oauth-protected-resource/mcp"
	protected := auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{Scopes: cfg.OAuth.RequiredScopes, ResourceMetadataURL: metadataURL})(view)
	mux := http.NewServeMux()
	metadata := auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource: cfg.PublicURL, AuthorizationServers: []string{cfg.OAuth.Issuer}, ScopesSupported: cfg.OAuth.RequiredScopes,
	})
	mux.Handle("/.well-known/oauth-protected-resource", metadata)
	mux.Handle("/.well-known/oauth-protected-resource/mcp", metadata)
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origins := r.Header.Values("Origin")
		if len(origins) > 1 || len(origins) == 1 && !slices.Contains(cfg.AllowedOrigins, origins[0]) {
			http.Error(w, "origin not allowed", 403)
			return
		}
		if len(origins) == 1 {
			w.Header().Set("Access-Control-Allow-Origin", origins[0])
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "POST, GET, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, MCP-Protocol-Version, MCP-Session-Id, Last-Event-ID")
			w.Header().Set("Access-Control-Expose-Headers", "WWW-Authenticate, MCP-Session-Id, MCP-Protocol-Version")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		protected.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if err := b.check(r.Context()); err != nil {
			http.Error(w, "backend unavailable or unprotected", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ready"}`))
	})
	return mux, nil
}

func toolResult(value map[string]any) *mcp.CallToolResult {
	data, _ := json.Marshal(value)
	return &mcp.CallToolResult{StructuredContent: value, Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}
}

func toolError(err error) *mcp.CallToolResult {
	status, message := 400, "invalid tool arguments"
	var e *apiError
	if errors.As(err, &e) {
		status, message = e.status, e.Error()
	}
	result := toolResult(map[string]any{"error": map[string]any{"code": status, "message": message}})
	result.IsError = true
	return result
}
