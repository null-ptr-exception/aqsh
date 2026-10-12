package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

type principal struct {
	identity string
	groups   []string
}

// Identity includes the trusted issuer to avoid collisions between providers.
func identity(issuer, subject string) string {
	return "oauth:" + issuer + "#" + url.QueryEscape(subject)
}

func newVerifier(ctx context.Context, cfg Config, client *http.Client) (auth.TokenVerifier, error) {
	u, _ := url.Parse(cfg.OAuth.Issuer)
	oauthDiscovery := *u
	oauthDiscovery.Path = "/.well-known/oauth-authorization-server" + u.Path
	var metadata struct {
		Issuer string `json:"issuer"`
		JWKS   string `json:"jwks_uri"`
	}
	var discoveryErr error
	for _, location := range []string{oauthDiscovery.String(), strings.TrimSuffix(cfg.OAuth.Issuer, "/") + "/.well-known/openid-configuration"} {
		discoveryErr = getJSON(ctx, client, location, &metadata)
		if discoveryErr == nil {
			break
		}
	}
	if discoveryErr != nil {
		return nil, errors.New("authorization server discovery failed")
	}
	if metadata.Issuer != cfg.OAuth.Issuer {
		return nil, errors.New("authorization server issuer mismatch")
	}
	if _, err := endpoint(metadata.JWKS); err != nil {
		return nil, errors.New("invalid authorization server jwks_uri")
	}
	keys := oidc.NewRemoteKeySet(oidc.ClientContext(ctx, client), metadata.JWKS)
	verifier := oidc.NewVerifier(cfg.OAuth.Issuer, keys, &oidc.Config{
		ClientID: cfg.PublicURL, SupportedSigningAlgs: []string{"RS256", "ES256", "PS256", "EdDSA"},
	})
	validator := jwt.NewValidator(jwt.WithIssuer(cfg.OAuth.Issuer), jwt.WithAudience(cfg.PublicURL), jwt.WithExpirationRequired())
	return func(ctx context.Context, raw string, _ *http.Request) (*auth.TokenInfo, error) {
		if len(raw) > 16384 {
			return nil, auth.ErrInvalidToken
		}
		token, err := verifier.Verify(ctx, raw)
		if err != nil {
			return nil, auth.ErrInvalidToken
		}
		var claims jwt.MapClaims
		if token.Claims(&claims) != nil || validator.Validate(claims) != nil || token.Subject == "" || len(token.Subject) > 1024 {
			return nil, auth.ErrInvalidToken
		}
		scopes := []string{}
		for _, name := range []string{"scope", "scp"} {
			switch value := claims[name].(type) {
			case string:
				scopes = append(scopes, strings.Fields(value)...)
			case []any:
				for _, item := range value {
					if scope, ok := item.(string); ok {
						scopes = append(scopes, scope)
					}
				}
			}
		}
		groups := []string{}
		if value, exists := claims[cfg.OAuth.GroupsClaim]; exists {
			list, ok := value.([]any)
			if !ok {
				return nil, auth.ErrInvalidToken
			}
			seen := map[string]bool{}
			for _, value := range list {
				group, ok := value.(string)
				if !ok {
					return nil, auth.ErrInvalidToken
				}
				if mapped := cfg.OAuth.GroupMapping[group]; mapped != "" && !seen[mapped] {
					groups = append(groups, mapped)
					seen[mapped] = true
				}
			}
		}
		sort.Strings(groups)
		p := principal{identity(cfg.OAuth.Issuer, token.Subject), groups}
		return &auth.TokenInfo{UserID: p.identity, Scopes: scopes, Expiration: token.Expiry, Extra: map[string]any{"principal": p}}, nil
	}, nil
}

func requestPrincipal(r *http.Request) principal {
	return auth.TokenInfoFromContext(r.Context()).Extra["principal"].(principal)
}

// Keep discovery parsing separate from verification; token-supplied URLs are never used.
func getJSON(ctx context.Context, client *http.Client, location string, value any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", location, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("discovery endpoint unavailable")
	}
	return json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(value)
}
