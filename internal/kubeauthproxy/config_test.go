package proxy

import "testing"

func TestConfig_Validate_RequiresUpstream(t *testing.T) {
	cfg := &Config{
		Port: 4180,
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error when --upstream is missing")
	}
}

func TestConfig_Validate_WithUpstream(t *testing.T) {
	cfg := &Config{
		Upstream: "http://localhost:8080",
		Port:     4180,
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

func TestConfig_Validate_WithTokenReviewURL(t *testing.T) {
	cfg := &Config{
		TokenReviewURL: "http://kube-federated-auth:8080",
		Upstream:       "http://localhost:8080",
		Port:           4180,
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

func TestConfig_NewTokenReviewer_WithURL(t *testing.T) {
	cfg := &Config{
		TokenReviewURL: "http://kube-federated-auth:8080",
		Upstream:       "http://localhost:8080",
	}
	reviewer, err := cfg.NewTokenReviewer()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "http://kube-federated-auth:8080/apis/authentication.k8s.io/v1/tokenreviews"
	if reviewer.url != want {
		t.Errorf("url = %q, want %q", reviewer.url, want)
	}
	if reviewer.bearerToken != "" {
		t.Errorf("bearerToken should be empty for custom URL, got %q", reviewer.bearerToken)
	}
}
