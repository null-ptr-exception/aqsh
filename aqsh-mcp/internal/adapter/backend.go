package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
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
		OwnerOnly     bool `json:"execution_owner_only"`
		JSONSupported bool `json:"result_json_supported"`
	}
	if err := b.request(ctx, "GET", "/health", principal{}, nil, &health); err != nil {
		return err
	}
	if !health.OwnerOnly || !health.JSONSupported {
		return errors.New("aqsh requires execution_owner_only and result_json_supported")
	}
	return nil
}

type taskDef struct {
	Description   string   `json:"description"`
	Inputs        []input  `json:"input"`
	AllowedUsers  []string `json:"allowed_users"`
	AllowedGroups []string `json:"allowed_groups"`
}

func (b *backend) definition(ctx context.Context, task string, p principal) (taskDef, error) {
	var d taskDef
	err := b.request(ctx, "GET", "/tasks/"+url.PathEscape(task), p, nil, &d)
	return d, err
}

func (d taskDef) allowed(p principal) bool {
	if len(d.AllowedUsers) == 0 && len(d.AllowedGroups) == 0 {
		return true
	}
	if slices.Contains(d.AllowedUsers, p.identity) {
		return true
	}
	for _, g := range p.groups {
		if slices.Contains(d.AllowedGroups, g) {
			return true
		}
	}
	return false
}

type execution struct {
	ID          string `json:"id"`
	Task        string `json:"task"`
	Status      string `json:"status"`
	Identity    string `json:"identity"`
	CreatedAt   string `json:"created_at"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	Result      *struct {
		ExitCode int             `json:"exit_code"`
		JSON     json.RawMessage `json:"json"`
		Omitted  string          `json:"json_omitted"`
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
	case len(e.Result.JSON) > t.MaxResultBytes:
		out["result_omitted"] = "too_large"
	case len(e.Result.JSON) == 0:
		// Do not relay arbitrary server errors as omission reasons.
		if slices.Contains([]string{"absent", "empty", "too_large", "invalid_json"}, e.Result.Omitted) {
			out["result_omitted"] = e.Result.Omitted
		} else {
			out["result_omitted"] = "unavailable"
		}
	default:
		var data any
		decoder := json.NewDecoder(bytes.NewReader(e.Result.JSON))
		decoder.UseNumber()
		if decoder.Decode(&data) != nil {
			out["result_omitted"] = "invalid_json"
		} else {
			out["data"] = data
		}
	}
	return out
}
