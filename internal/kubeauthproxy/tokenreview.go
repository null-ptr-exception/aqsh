package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

const ExtraKeyClusterName = "authentication.kubernetes.io/cluster-name"

// TokenReview types — lightweight replacements for k8s.io/api/authentication/v1.
// Only the fields we need for JSON serialization.

type TokenReviewRequest struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Spec       TokenReviewSpec `json:"spec"`
}

type TokenReviewSpec struct {
	Token     string   `json:"token"`
	Audiences []string `json:"audiences,omitempty"`
}

type TokenReviewResponse struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Status     TokenReviewStatus `json:"status"`
}

type TokenReviewStatus struct {
	Authenticated bool     `json:"authenticated"`
	Audiences     []string `json:"audiences,omitempty"`
	User          UserInfo `json:"user,omitempty"`
	Error         string   `json:"error,omitempty"`
}

type UserInfo struct {
	Username string              `json:"username"`
	UID      string              `json:"uid,omitempty"`
	Groups   []string            `json:"groups,omitempty"`
	Extra    map[string][]string `json:"extra,omitempty"`
}

// TokenReviewer validates tokens via the TokenReview API.
type TokenReviewer interface {
	Review(ctx context.Context, token string) (*TokenReviewResponse, error)
}

// HTTPTokenReviewer calls the TokenReview API using a plain HTTP client.
type HTTPTokenReviewer struct {
	url         string // full URL to POST TokenReview requests
	client      *http.Client
	tokenFile   string
	audiences   []string
	bearerToken string // optional: for authenticating to the API server
}

func (r *HTTPTokenReviewer) Review(ctx context.Context, token string) (*TokenReviewResponse, error) {
	reqBody := TokenReviewRequest{
		APIVersion: "authentication.k8s.io/v1",
		Kind:       "TokenReview",
		Spec:       TokenReviewSpec{Token: token, Audiences: r.audiences},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling token review request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	bearer := r.bearerToken
	if r.tokenFile != "" {
		data, err := os.ReadFile(r.tokenFile)
		if err != nil {
			return nil, fmt.Errorf("reading reviewer token: %w", err)
		}
		bearer = strings.TrimSpace(string(data))
		if bearer == "" || strings.ContainsAny(bearer, "\r\n \t") {
			return nil, fmt.Errorf("invalid reviewer token")
		}
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling token review: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		var status struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&status); err == nil && status.Message != "" {
			return nil, fmt.Errorf("%s", status.Message)
		}
		return nil, fmt.Errorf("token review returned status %d", resp.StatusCode)
	}
	if resp.StatusCode >= 500 {
		body, _ := io.ReadAll(resp.Body)
		slog.Error("token review API error", "status", resp.StatusCode, "body", string(body))
		return nil, fmt.Errorf("token review returned status %d", resp.StatusCode)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token review returned status %d", resp.StatusCode)
	}
	var result TokenReviewResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding token review response: %w", err)
	}

	return &result, nil
}

// NewHTTPTokenReviewer adapts the existing client for embedding without starting
// an HTTP reverse proxy. The caller owns HTTP client TLS and timeout settings.
func NewHTTPTokenReviewer(endpoint string, client *http.Client, tokenFile string, audiences []string) *HTTPTokenReviewer {
	return &HTTPTokenReviewer{url: endpoint, client: client, tokenFile: tokenFile, audiences: append([]string(nil), audiences...)}
}
