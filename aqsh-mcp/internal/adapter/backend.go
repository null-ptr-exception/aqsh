package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

type backend struct {
	url    string
	client *http.Client
}
type apiError struct{ status int }

func (e *apiError) Error() string { return "aqsh request rejected or unavailable" }

func (b *backend) request(ctx context.Context, method, path string, p principal, body, out any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, b.url+path, bytes.NewReader(data))
	if err != nil {
		return &apiError{502}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// Construct fresh headers. Never forward caller identity headers or access tokens.
	req.Header.Set("X-Forwarded-User", p.identity)
	req.Header.Set("X-Forwarded-Groups", strings.Join(p.groups, ","))
	resp, err := b.client.Do(req)
	if err != nil {
		return &apiError{502}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 202 {
		return &apiError{resp.StatusCode}
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 8<<20)).Decode(out); err != nil {
		return &apiError{502}
	}
	return nil
}

func (b *backend) check(ctx context.Context) error {
	var health struct {
		RBACEnabled bool   `json:"rbac_enabled"`
		Redis       string `json:"redis"`
	}
	if err := b.request(ctx, "GET", "/health", principal{}, nil, &health); err != nil {
		return err
	}
	if !health.RBACEnabled {
		return errors.New("aqsh requires RBAC to be enabled")
	}
	if health.Redis != "connected" {
		return errors.New("aqsh backend unavailable")
	}
	return nil
}

type taskDef struct {
	Description string  `json:"description"`
	Inputs      []input `json:"input"`
}

func (b *backend) definition(ctx context.Context, task string, p principal) (taskDef, error) {
	var d taskDef
	err := b.request(ctx, "GET", "/tasks/"+url.PathEscape(task), p, nil, &d)
	return d, err
}

type execution struct {
	ID          string `json:"id"`
	Task        string `json:"task"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	Result      *struct {
		ExitCode int     `json:"exit_code"`
		Data     *string `json:"data"`
	} `json:"result"`
}

func (e execution) output(t ToolConfig) map[string]any {
	out := map[string]any{"execution_id": e.ID, "status": e.Status}
	for key, value := range map[string]string{"created_at": e.CreatedAt, "started_at": e.StartedAt, "completed_at": e.CompletedAt} {
		if value != "" {
			out[key] = value
		}
	}
	if e.Status != "completed" && e.Status != "failed" {
		out["poll_after_ms"] = 1000
		return out
	}
	if e.Result != nil {
		out["exit_code"] = e.Result.ExitCode
	}
	switch {
	case t.ResultMode != "json":
		out["result_omitted"] = "not_enabled"
	case e.Result == nil:
		out["result_omitted"] = "unavailable"
	case e.Result.Data == nil:
		out["result_omitted"] = "absent"
	case len(*e.Result.Data) > t.MaxResultBytes:
		out["result_omitted"] = "too_large"
	case strings.TrimSpace(*e.Result.Data) == "":
		out["result_omitted"] = "empty"
	case !json.Valid([]byte(*e.Result.Data)):
		out["result_omitted"] = "invalid_json"
	default:
		// Keep null and number precision intact when encoding the MCP response.
		out["data"] = json.RawMessage(*e.Result.Data)
	}
	return out
}
