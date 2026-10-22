package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPTokenReviewer_Authenticated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q, want %q", ct, "application/json")
		}

		var req TokenReviewRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req.Spec.Token != "test-token" {
			t.Errorf("token = %q, want %q", req.Spec.Token, "test-token")
		}

		resp := TokenReviewResponse{
			APIVersion: "authentication.k8s.io/v1",
			Kind:       "TokenReview",
			Status: TokenReviewStatus{
				Authenticated: true,
				User: UserInfo{
					Username: "system:serviceaccount:default:test",
					Groups:   []string{"system:serviceaccounts", "system:serviceaccounts:default"},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	reviewer := &HTTPTokenReviewer{
		url:    server.URL,
		client: server.Client(),
	}

	result, err := reviewer.Review(context.Background(), "test-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Status.Authenticated {
		t.Error("expected authenticated = true")
	}
	if result.Status.User.Username != "system:serviceaccount:default:test" {
		t.Errorf("username = %q, want %q", result.Status.User.Username, "system:serviceaccount:default:test")
	}
}

func TestHTTPTokenReviewer_Unauthenticated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := TokenReviewResponse{
			APIVersion: "authentication.k8s.io/v1",
			Kind:       "TokenReview",
			Status: TokenReviewStatus{
				Authenticated: false,
				Error:         "token not valid",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	reviewer := &HTTPTokenReviewer{
		url:    server.URL,
		client: server.Client(),
	}

	result, err := reviewer.Review(context.Background(), "bad-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status.Authenticated {
		t.Error("expected authenticated = false")
	}
	if result.Status.Error != "token not valid" {
		t.Errorf("error = %q, want %q", result.Status.Error, "token not valid")
	}
}

func TestHTTPTokenReviewer_BearerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer my-sa-token" {
			t.Errorf("Authorization = %q, want %q", auth, "Bearer my-sa-token")
		}

		resp := TokenReviewResponse{
			Status: TokenReviewStatus{Authenticated: true, User: UserInfo{Username: "test"}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	reviewer := &HTTPTokenReviewer{
		url:         server.URL,
		client:      server.Client(),
		bearerToken: "my-sa-token",
	}

	_, err := reviewer.Review(context.Background(), "some-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPTokenReviewer_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	reviewer := &HTTPTokenReviewer{
		url:    server.URL,
		client: server.Client(),
	}

	_, err := reviewer.Review(context.Background(), "some-token")
	if err == nil {
		t.Fatal("expected error for server error response")
	}
}

func TestHTTPTokenReviewer_Forbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"kind":       "Status",
			"apiVersion": "v1",
			"status":     "Failure",
			"message":    "tokenreviews.authentication.k8s.io is forbidden",
			"reason":     "Forbidden",
			"code":       403,
		})
	}))
	defer server.Close()

	reviewer := &HTTPTokenReviewer{
		url:    server.URL,
		client: server.Client(),
	}

	_, err := reviewer.Review(context.Background(), "some-token")
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
	want := "tokenreviews.authentication.k8s.io is forbidden"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}
