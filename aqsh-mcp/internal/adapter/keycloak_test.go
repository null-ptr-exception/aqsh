package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Optional acceptance fixture: all accounts, passwords and realm data are
// synthetic and exist only inside this test's disposable Keycloak container.
type keycloakProvider struct {
	issuer   string
	subjects map[string]string
}

func startKeycloak(t *testing.T, image, audience string) keycloakProvider {
	t.Helper()
	name := fmt.Sprintf("aqsh-mcp-oauth-%d", time.Now().UnixNano())
	base := "http://" + unusedAddress(t)
	endpoint, _ := url.Parse(base)
	p := keycloakProvider{issuer: base + "/realms/aqsh-mcp", subjects: map[string]string{}}
	users := []any{}
	for n, user := range []string{"alice", "bob", "auditor"} {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", n+1)
		p.subjects[user] = id
		group := "ops"
		if user == "auditor" {
			group = "auditors"
		}
		users = append(users, map[string]any{
			"id": id, "username": user, "enabled": true, "emailVerified": true,
			"firstName": user, "lastName": "Test", "email": user + "@example.invalid",
			"groups":      []string{"/" + group},
			"credentials": []any{map[string]any{"type": "password", "value": "test-only-password", "temporary": false}},
		})
	}
	realm := map[string]any{
		"realm": "aqsh-mcp", "enabled": true, "sslRequired": "none", "users": users,
		"groups": []any{map[string]any{"name": "ops"}, map[string]any{"name": "auditors"}},
		"clientScopes": []any{map[string]any{"name": "aqsh:read", "protocol": "openid-connect",
			"attributes": map[string]string{"include.in.token.scope": "true"}}},
		"clients": []any{map[string]any{
			"clientId": "test-client", "enabled": true, "publicClient": true,
			"protocol": "openid-connect", "standardFlowEnabled": true, "directAccessGrantsEnabled": false, "fullScopeAllowed": false,
			"redirectUris": []string{"http://127.0.0.1/callback"}, "defaultClientScopes": []string{"aqsh:read"},
			"attributes": map[string]string{"pkce.code.challenge.method": "S256"},
			"protocolMappers": []any{
				map[string]any{"name": "subject", "protocol": "openid-connect", "protocolMapper": "oidc-sub-mapper",
					"config": map[string]string{"access.token.claim": "true"}},
				map[string]any{"name": "mcp-audience", "protocol": "openid-connect", "protocolMapper": "oidc-audience-mapper",
					"config": map[string]string{"included.custom.audience": audience, "access.token.claim": "true", "id.token.claim": "false"}},
				map[string]any{"name": "groups", "protocol": "openid-connect", "protocolMapper": "oidc-group-membership-mapper",
					"config": map[string]string{"claim.name": "groups", "full.path": "false", "access.token.claim": "true", "id.token.claim": "false"}},
			},
		}},
	}
	dir := filepath.Join(t.TempDir(), "import")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(realm)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "realm.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	docker := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(t.Context(), "docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("docker %s: %v\n%s", args[0], err, out)
		}
	}
	docker("create", "--name", name, "-p", "127.0.0.1:"+endpoint.Port()+":8080", image,
		"start-dev", "--import-realm", "--hostname", base)
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Logf("Keycloak output:\n%s", out)
		}
		if out, err := exec.Command("docker", "rm", "-f", name).CombinedOutput(); err != nil {
			t.Errorf("remove Keycloak container: %v\n%s", err, out)
		}
	})
	docker("cp", dir, name+":/opt/keycloak/data/")
	docker("start", name)
	waitHTTPTimeout(t, p.issuer+"/.well-known/openid-configuration", 90*time.Second)
	return p
}

func (p keycloakProvider) connect(t *testing.T, endpoint, user string) *mcp.ClientSession {
	t.Helper()
	return connectOAuthWithFetcher(t, endpoint, func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		jar, _ := cookiejar.New(nil)
		client := &http.Client{Jar: jar, Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, _ := http.NewRequestWithContext(ctx, "GET", args.URL, nil)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		page, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			return nil, fmt.Errorf("Keycloak login page: status=%d error=%v", resp.StatusCode, err)
		}
		action := ""
		for _, form := range regexp.MustCompile(`<form\b[^>]*>`).FindAllString(string(page), -1) {
			if strings.Contains(form, `id="kc-form-login"`) {
				if match := regexp.MustCompile(`action="([^"]+)"`).FindStringSubmatch(form); len(match) == 2 {
					action = html.UnescapeString(match[1])
				}
			}
		}
		if action == "" {
			return nil, fmt.Errorf("Keycloak login form not found")
		}
		form := url.Values{"username": {user}, "password": {"test-only-password"}}
		req, _ = http.NewRequestWithContext(ctx, "POST", action, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err = client.Do(req)
		if err != nil {
			return nil, err
		}
		resp.Body.Close()
		if resp.StatusCode != 302 {
			return nil, fmt.Errorf("Keycloak login callback status %d", resp.StatusCode)
		}
		callback, err := url.Parse(resp.Header.Get("Location"))
		if err != nil || callback.Scheme != "http" || callback.Host != "127.0.0.1" || callback.Path != "/callback" {
			return nil, fmt.Errorf("unexpected Keycloak callback")
		}
		q := callback.Query()
		return &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, nil
	})
}
