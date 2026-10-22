package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"

	proxy "github.com/rophy/aqsh/internal/kubeauthproxy"
)

type Kubernetes struct {
	cluster   string
	config    ClusterConfig
	audiences []string
	client    *http.Client
	reviewer  proxy.TokenReviewer
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
		k.reviewer = proxy.NewHTTPTokenReviewer(strings.TrimRight(cluster.APIServer, "/")+"/apis/authentication.k8s.io/v1/tokenreviews", client, cluster.TokenFile, k.audiences)
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
	result, err := k.reviewer.Review(ctx, token)
	if err != nil {
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
