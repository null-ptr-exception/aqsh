package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	clusterExtraKey      = "authentication.kubernetes.io/cluster-name"
	maxConcurrentReviews = 32
	maxCachedReviews     = 256
)

type Kubernetes struct {
	config   Config
	client   *http.Client
	inFlight chan struct{}
	mu       sync.Mutex
	cache    map[[sha256.Size]byte]cachedReview
	now      func() time.Time
}

type cachedReview struct {
	principal Principal
	expires   time.Time
}

type tokenReview struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Spec       struct {
		Token     string   `json:"token"`
		Audiences []string `json:"audiences"`
	} `json:"spec"`
	Status *struct {
		Authenticated bool     `json:"authenticated"`
		Audiences     []string `json:"audiences"`
		User          userInfo `json:"user"`
		Error         string   `json:"error"`
	} `json:"status,omitempty"`
}

type userInfo struct {
	Username string              `json:"username"`
	Groups   []string            `json:"groups"`
	Extra    map[string][]string `json:"extra"`
}

func New(cfg Config) (*Kubernetes, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	client, err := newClient(cfg.TokenReview.CAFile, cfg)
	if err != nil {
		return nil, err
	}
	if _, err := readToken(cfg.TokenReview.TokenFile); err != nil {
		return nil, err
	}
	cfg.Audiences = slices.Clone(cfg.Audiences)
	return &Kubernetes{config: cfg, client: client, inFlight: make(chan struct{}, maxConcurrentReviews),
		cache: make(map[[sha256.Size]byte]cachedReview), now: time.Now}, nil
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

// Authenticate caches successful reviews for a fixed, bounded lifetime. Credentials
// are never stored in the cache, and failed reviews never reuse expired entries.
func (k *Kubernetes) Authenticate(ctx context.Context, token string) (Principal, error) {
	if ctx.Err() != nil {
		return Principal{}, ErrUnavailable
	}
	if k.config.CacheTTL == 0 {
		return k.review(ctx, token)
	}
	key, started := sha256.Sum256([]byte(token)), k.now()
	k.mu.Lock()
	entry, ok := k.cache[key]
	k.mu.Unlock()
	if ok && k.now().Before(entry.expires) {
		entry.principal.Groups = slices.Clone(entry.principal.Groups)
		return entry.principal, nil
	}
	p, err := k.review(ctx, token)
	if err != nil {
		return Principal{}, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	for hash, entry := range k.cache {
		if !now.Before(entry.expires) {
			delete(k.cache, hash)
		}
	}
	// Keep an existing entry's deadline even if concurrent cache misses succeed.
	if _, exists := k.cache[key]; !exists && len(k.cache) < maxCachedReviews && now.Before(started.Add(k.config.CacheTTL)) {
		cached := p
		cached.Groups = slices.Clone(p.Groups)
		k.cache[key] = cachedReview{cached, started.Add(k.config.CacheTTL)}
	}
	return p, nil
}

// review bounds in-flight requests, including the capacity wait in the timeout.
func (k *Kubernetes) review(ctx context.Context, token string) (Principal, error) {
	ctx, cancel := context.WithTimeout(ctx, k.client.Timeout)
	defer cancel()
	select {
	case k.inFlight <- struct{}{}:
		defer func() { <-k.inFlight }()
	case <-ctx.Done():
		return Principal{}, ErrUnavailable
	}
	reviewer, err := readToken(k.config.TokenReview.TokenFile)
	if err != nil {
		return Principal{}, ErrUnavailable
	}
	tr := tokenReview{APIVersion: "authentication.k8s.io/v1", Kind: "TokenReview"}
	tr.Spec.Token, tr.Spec.Audiences = token, k.config.Audiences
	body, _ := json.Marshal(tr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(k.config.TokenReview.URL, "/")+"/apis/authentication.k8s.io/v1/tokenreviews", bytes.NewReader(body))
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
	if status == nil {
		return Principal{}, ErrUnavailable
	}
	// Existing task environments and values_url represent groups as CSV. Reject
	// ambiguous names rather than turn one verified group into multiple groups.
	for _, group := range status.User.Groups {
		if group == "" || strings.TrimSpace(group) != group || strings.ContainsAny(group, ",\r\n\x00") {
			return Principal{}, ErrUnauthenticated
		}
	}
	if !status.Authenticated || status.Error != "" || status.User.Username == "" || strings.ContainsAny(status.User.Username, "\r\n\x00") {
		return Principal{}, ErrUnauthenticated
	}
	if !slices.ContainsFunc(status.Audiences, func(a string) bool { return slices.Contains(k.config.Audiences, a) }) {
		return Principal{}, ErrUnauthenticated
	}
	cluster := k.config.ClusterName
	if k.config.TrustClusterExtra {
		names := status.User.Extra[clusterExtraKey]
		if len(names) != 1 || !validClusterName(names[0]) {
			return Principal{}, ErrUnauthenticated
		}
		cluster = names[0]
	}
	p := Principal{Identity: status.User.Username, Groups: status.User.Groups}
	if k.config.IncludeClusterInIdentity {
		p.Identity = cluster + "/" + p.Identity
		for i := range p.Groups {
			p.Groups[i] = cluster + "/" + p.Groups[i]
		}
	}
	return p, nil
}

func (k *Kubernetes) Close() { k.client.CloseIdleConnections() }
