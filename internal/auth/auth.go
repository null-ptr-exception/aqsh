// Package auth authenticates callers independently of HTTP routing and task execution.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

var (
	ErrUnauthenticated = errors.New("invalid credentials")
	ErrUnavailable     = errors.New("authentication unavailable")
)

// Principal contains verified identity only. It never contains bearer credentials.
// Cluster is an operator-assigned trust domain, not a token-supplied claim.
type Principal struct {
	Provider string   `json:"provider"`
	Cluster  string   `json:"cluster"`
	Username string   `json:"username"`
	UID      string   `json:"uid"`
	Groups   []string `json:"groups,omitempty"`
}

type Authenticator interface {
	Authenticate(context.Context, string) (Principal, error)
}

type principalKey struct{}

func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	p.Groups = append([]string(nil), p.Groups...)
	return p, ok
}

// Middleware ignores forwarded identity in native mode. The only unauthenticated
// endpoints are the exact GET health and metrics routes used by probes/scrapers.
func Middleware(a Authenticator, identityHeader, groupsHeader string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (r.URL.Path == "/health" || r.URL.Path == "/metrics") {
			next.ServeHTTP(w, r)
			return
		}
		values := r.Header.Values("Authorization")
		for _, name := range []string{identityHeader, groupsHeader, "X-Forwarded-User", "X-Forwarded-Groups", "X-Forwarded-Cluster"} {
			r.Header.Del(name)
		}
		r.Header.Del("Authorization")
		if len(values) != 1 {
			unauthorized(w)
			return
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > 16384 {
			unauthorized(w)
			return
		}
		p, err := a.Authenticate(r.Context(), parts[1])
		if err != nil {
			if errors.Is(err, ErrUnauthenticated) {
				unauthorized(w)
			} else {
				writeError(w, http.StatusServiceUnavailable, "authentication unavailable")
			}
			return
		}
		p.Groups = append([]string(nil), p.Groups...)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, "invalid credentials")
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
