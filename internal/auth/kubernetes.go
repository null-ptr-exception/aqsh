package auth

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
)

type Kubernetes struct {
	cluster   string
	config    ClusterConfig
	audiences []string
	client    *http.Client
}

type tokenReview struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Spec       struct {
		Token     string   `json:"token"`
		Audiences []string `json:"audiences"`
	} `json:"spec"`
	Status struct {
		Authenticated bool     `json:"authenticated"`
		Audiences     []string `json:"audiences"`
		User          userInfo `json:"user"`
		Error         string   `json:"error"`
	} `json:"status,omitempty"`
}

type userInfo struct {
	Username string   `json:"username"`
	UID      string   `json:"uid"`
	Groups   []string `json:"groups"`
}

func New(cfg Config) (*Kubernetes, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	k := &Kubernetes{audiences: append([]string(nil), cfg.Audiences...)}
	for id, cluster := range cfg.Clusters {
		client, err := newClient(cluster.CAFile, cfg)
		if err != nil {
			return nil, fmt.Errorf("cluster %q: %w", id, err)
		}
		if _, err := readToken(cluster.TokenFile); err != nil {
			return nil, fmt.Errorf("cluster %q: %w", id, err)
		}
		k.cluster, k.config, k.client = id, cluster, client
	}
	return k, nil
}

func newClient(caFile string, cfg Config) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		ca, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("reading CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			return nil, fmt.Errorf("invalid CA certificate")
		}
		transport.TLSClientConfig.RootCAs = pool
	}
	return &http.Client{
		Transport: transport,
		Timeout:   cfg.RequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func readToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading reviewer token: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.ContainsAny(token, "\r\n \t") {
		return "", fmt.Errorf("empty or malformed reviewer token")
	}
	return token, nil
}

func (k *Kubernetes) Authenticate(ctx context.Context, token string) (Principal, error) {
	reviewer, err := readToken(k.config.TokenFile)
	if err != nil {
		return Principal{}, ErrUnavailable
	}
	tr := tokenReview{APIVersion: "authentication.k8s.io/v1", Kind: "TokenReview"}
	tr.Spec.Token, tr.Spec.Audiences = token, k.audiences
	body, _ := json.Marshal(tr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(k.config.APIServer, "/")+"/apis/authentication.k8s.io/v1/tokenreviews", bytes.NewReader(body))
	if err != nil {
		return Principal{}, ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+reviewer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.client.Do(req)
	if err != nil {
		return Principal{}, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Principal{}, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	var result tokenReview
	if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &result) != nil {
		return Principal{}, ErrUnavailable
	}
	status := result.Status
	// Existing task environments and values_url represent groups as CSV. Reject
	// ambiguous names rather than turn one verified group into multiple groups.
	for _, group := range status.User.Groups {
		if group == "" || strings.TrimSpace(group) != group || strings.ContainsAny(group, ",\r\n") {
			return Principal{}, ErrUnauthenticated
		}
	}
	if !status.Authenticated || status.Error != "" || status.User.Username == "" || status.User.UID == "" {
		return Principal{}, ErrUnauthenticated
	}
	if !slices.ContainsFunc(status.Audiences, func(a string) bool { return slices.Contains(k.audiences, a) }) {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{Provider: "kubernetes", Cluster: k.cluster, Username: status.User.Username, UID: status.User.UID, Groups: status.User.Groups}, nil
}

func (k *Kubernetes) Close() { k.client.CloseIdleConnections() }
