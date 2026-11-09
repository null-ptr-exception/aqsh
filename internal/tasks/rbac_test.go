package tasks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRBAC(t *testing.T) {
	for _, tc := range []struct {
		name, policy, wantError string
		enabled                 bool
	}{
		{name: "legacy"},
		{name: "empty roles deny", policy: "roles: {}\n", enabled: true},
		{name: "empty bindings deny", policy: "bindings: {}\n", enabled: true},
		{name: "unbound role", policy: "roles: {runner: [{tasks: [deploy], verbs: [run]}]}\n", enabled: true},
		{name: "empty role", policy: "roles: {runner: []}\nbindings: {runner: {users: [alice]}}\n", enabled: true},
		{name: "no subjects", policy: "roles: {runner: [{tasks: [deploy], verbs: [run]}]}\nbindings: {runner: {}}\n", enabled: true},
		{name: "valid", policy: "roles: {runner: [{tasks: [deploy, retired], verbs: [run, read]}]}\nbindings: {runner: {users: [alice], groups: [ops]}}\n", enabled: true},
		{name: "wildcard task", policy: "roles: {runner: [{tasks: ['*'], verbs: [read]}]}\n", enabled: true},
		{name: "null roles", policy: "roles: null\n", wantError: "roles must be a mapping"},
		{name: "null bindings", policy: "bindings:\n", wantError: "bindings must be a mapping"},
		{name: "aliased null", policy: "defaults: &empty null\nroles: *empty\n", wantError: "roles must be a mapping"},
		{name: "unknown role", policy: "bindings: {missing: {users: [alice]}}\n", wantError: "unknown role"},
		{name: "unknown verb", policy: "roles: {runner: [{tasks: [deploy], verbs: [delete]}]}\n", wantError: "unknown verb"},
		{name: "verb wildcard", policy: "roles: {runner: [{tasks: [deploy], verbs: ['*']}]}\n", wantError: "unknown verb"},
		{name: "empty task selectors", policy: "roles: {runner: [{tasks: [], verbs: [run]}]}\n", wantError: "requires tasks and verbs"},
		{name: "empty verbs", policy: "roles: {runner: [{tasks: [deploy], verbs: []}]}\n", wantError: "requires tasks and verbs"},
		{name: "blank selector", policy: "roles: {runner: [{tasks: [' '], verbs: [run]}]}\n", wantError: "invalid task selector"},
		{name: "partial wildcard", policy: "roles: {runner: [{tasks: ['deploy-*'], verbs: [run]}]}\n", wantError: "invalid task selector"},
		{name: "blank role", policy: "roles: {' ': []}\n", wantError: "invalid role name"},
		{name: "blank subject", policy: "roles: {runner: []}\nbindings: {runner: {users: [' ']}}\n", wantError: "subjects must be nonempty"},
		{name: "padded subject", policy: "roles: {runner: []}\nbindings: {runner: {groups: [' ops ']}}\n", wantError: "subjects must be nonempty"},
		{name: "misspelled top level", policy: "role: {}\n", wantError: "field role not found"},
		{name: "misspelled rule", policy: "roles: {runner: [{task: [deploy], verbs: [run]}]}\n", wantError: "field task not found"},
		{name: "misspelled binding", policy: "roles: {runner: []}\nbindings: {runner: {user: [alice]}}\n", wantError: "field user not found"},
		{name: "duplicate role", policy: "roles:\n  runner: []\n  runner: []\n", wantError: "already defined"},
		{name: "second document", policy: "---\nroles: {}\n", wantError: "single YAML document"},
		{name: "mixed user ACL", policy: "roles: {}\ntasks: {deploy: {script: deploy.sh, allowed_users: [alice]}}\n", wantError: "cannot be combined"},
		{name: "mixed group ACL", policy: "roles: {}\ntasks: {deploy: {script: deploy.sh, allowed_groups: [ops]}}\n", wantError: "cannot be combined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "tasks.yaml")
			body := tc.policy
			if !strings.Contains(body, "tasks: {deploy:") {
				body = "tasks: {deploy: {script: deploy.sh}}\n" + body
			}
			if err := os.WriteFile(file, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(file)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("Load error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.RBACEnabled() != tc.enabled {
				t.Fatalf("RBACEnabled = %v, want %v", cfg.RBACEnabled(), tc.enabled)
			}
			if tc.name != "valid" && cfg.Allows("deploy", "run", "alice", []string{"ops"}) {
				t.Fatal("empty/unbound/legacy policy granted run")
			}
		})
	}
}

func TestRBACIncludes(t *testing.T) {
	for _, tc := range []struct{ name, included, wantError string }{
		{"tasks only", "tasks: {deploy: {script: deploy.sh}}\n", ""},
		{"empty file", "", ""},
		{"whitespace", " \n  \n", ""},
		{"comments only", "# no tasks yet\n", ""},
		{"mixed ACL", "tasks: {deploy: {script: deploy.sh, allowed_groups: [ops]}}\n", "cannot be combined"},
		{"included roles", "roles: {}\n", "must not define roles or bindings"},
		{"included bindings", "bindings: {}\n", "must not define roles or bindings"},
		{"included null", "roles: null\n", "roles must be a mapping"},
		{"included typo", "role: {}\n", "field role not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{
				"tasks.yaml":    "include: [included.yaml]\nroles: {runner: [{tasks: [deploy], verbs: [run]}]}\nbindings: {runner: {groups: [ops]}}\n",
				"included.yaml": tc.included,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := Load(filepath.Join(dir, "tasks.yaml"))
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("Load error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil || !cfg.Allows("deploy", "run", "alice", []string{"ops"}) {
				t.Fatalf("root policy did not apply to included task: %v", err)
			}
		})
	}
}

func TestLoadEmptyConfig(t *testing.T) {
	for _, body := range []string{"", " \n\n", "# no tasks yet\n"} {
		file := filepath.Join(t.TempDir(), "tasks.yaml")
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(file)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.RBACEnabled() || len(cfg.Tasks) != 0 {
			t.Fatalf("empty file changed legacy configuration: %+v", cfg)
		}
	}
}

func TestRBACGrants(t *testing.T) {
	cfg := &TasksConfig{
		Roles: map[string][]Rule{
			"runner":    {{Tasks: []string{"deploy"}, Verbs: []string{"run"}}},
			"reader":    {{Tasks: []string{"retired", "deploy"}, Verbs: []string{"read"}}},
			"qualified": {{Tasks: []string{"backup"}, Verbs: []string{"run"}}},
			"operator":  {{Tasks: []string{"*"}, Verbs: []string{"read", "run"}}},
		},
		Bindings: map[string]Binding{
			"runner":    {Users: []string{"alice"}, Groups: []string{"ci"}},
			"reader":    {Groups: []string{"ops"}},
			"qualified": {Users: []string{"a/system:serviceaccount:ci:runner"}, Groups: []string{"a/system:serviceaccounts:ci"}},
			"operator":  {Users: []string{"operator"}},
		},
	}
	for _, tc := range []struct {
		name, identity, task, verb string
		groups                     []string
		want                       bool
	}{
		{"exact user", "alice", "deploy", "run", nil, true},
		{"group grants", "bob", "deploy", "run", []string{"ci"}, true},
		{"user OR groups", "alice", "deploy", "run", []string{"other"}, true},
		{"run does not grant read", "alice", "deploy", "read", nil, false},
		{"read does not grant run", "bob", "deploy", "run", []string{"ops"}, false},
		{"additive read", "alice", "deploy", "read", []string{"ops"}, true},
		{"additive run", "alice", "deploy", "run", []string{"ops"}, true},
		{"deleted task remains grantable", "bob", "retired", "read", []string{"ops"}, true},
		{"wildcard future task", "operator", "future/task", "run", nil, true},
		{"other task denied", "alice", "backup", "run", nil, false},
		{"padded identity differs", " alice ", "deploy", "run", nil, false},
		{"case sensitive", "Alice", "deploy", "run", nil, false},
		{"missing identity", "", "deploy", "run", []string{"ci"}, false},
		{"blank identity", " ", "deploy", "run", []string{"ci"}, false},
		{"qualified user", "a/system:serviceaccount:ci:runner", "backup", "run", nil, true},
		{"qualified group", "other", "backup", "run", []string{"a/system:serviceaccounts:ci"}, true},
		{"other cluster user", "b/system:serviceaccount:ci:runner", "backup", "run", nil, false},
		{"other cluster group", "other", "backup", "run", []string{"b/system:serviceaccounts:ci"}, false},
		{"unqualified group", "other", "backup", "run", []string{"system:serviceaccounts:ci"}, false},
		{"group is not CSV", "other", "deploy", "run", []string{"other,ci"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cfg.Allows(tc.task, tc.verb, tc.identity, tc.groups); got != tc.want {
				t.Fatalf("Allows = %v, want %v", got, tc.want)
			}
		})
	}
}
