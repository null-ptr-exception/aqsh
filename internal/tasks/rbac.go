package tasks

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type Rule struct {
	Tasks []string `yaml:"tasks"`
	Verbs []string `yaml:"verbs"`
}

type Binding struct {
	Users  []string `yaml:"users"`
	Groups []string `yaml:"groups"`
}

// RBACEnabled distinguishes an explicitly empty policy from legacy configuration.
func (c *TasksConfig) RBACEnabled() bool {
	return c != nil && (c.Roles != nil || c.Bindings != nil)
}

// Allows evaluates additive, exact user/group grants. Only task names support "*".
// The caller must supply groups from the authenticated principal, not task input.
func (c *TasksConfig) Allows(task, verb, identity string, groups []string) bool {
	if strings.TrimSpace(identity) == "" {
		return false
	}
	for name, binding := range c.Bindings {
		if !slices.Contains(binding.Users, identity) && !slices.ContainsFunc(binding.Groups, func(g string) bool { return slices.Contains(groups, g) }) {
			continue
		}
		for _, rule := range c.Roles[name] {
			if slices.Contains(rule.Verbs, verb) && (slices.Contains(rule.Tasks, task) || slices.Contains(rule.Tasks, "*")) {
				return true
			}
		}
	}
	return false
}

func (c *TasksConfig) validateRBAC() error {
	if !c.RBACEnabled() {
		return nil
	}
	for name, task := range c.Tasks {
		if len(task.AllowedUsers) > 0 || len(task.AllowedGroups) > 0 {
			return fmt.Errorf("task %q: allowed_users/allowed_groups cannot be combined with roles or bindings", name)
		}
	}
	for name, rules := range c.Roles {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
			return fmt.Errorf("invalid role name %q", name)
		}
		for _, rule := range rules {
			if len(rule.Tasks) == 0 || len(rule.Verbs) == 0 {
				return fmt.Errorf("role %q: each rule requires tasks and verbs", name)
			}
			for _, task := range rule.Tasks {
				if strings.TrimSpace(task) == "" || strings.TrimSpace(task) != task || task != "*" && strings.Contains(task, "*") {
					return fmt.Errorf("role %q: invalid task selector %q (use exact names or *)", name, task)
				}
			}
			for _, verb := range rule.Verbs {
				if verb != "read" && verb != "run" {
					return fmt.Errorf("role %q: unknown verb %q", name, verb)
				}
			}
		}
	}
	for name, binding := range c.Bindings {
		if _, ok := c.Roles[name]; !ok {
			return fmt.Errorf("binding %q references an unknown role", name)
		}
		for _, subject := range append(append([]string(nil), binding.Users...), binding.Groups...) {
			if strings.TrimSpace(subject) == "" || strings.TrimSpace(subject) != subject || strings.ContainsAny(subject, "\r\n\x00") {
				return fmt.Errorf("binding %q: subjects must be nonempty exact identities/groups without surrounding whitespace or control characters", name)
			}
		}
	}
	return nil
}

// Strict decoding prevents misspelled policy keys or a second document from
// silently discarding authorization. Null policy sections are configuration errors.
func decodeConfig(data []byte) (*TasksConfig, error) {
	var cfg TasksConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("expected a single YAML document")
	}
	var fields map[string]yaml.Node
	if err := yaml.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for _, name := range []string{"roles", "bindings"} {
		node := fields[name]
		for node.Kind == yaml.AliasNode {
			node = *node.Alias
		}
		if node.Tag == "!!null" {
			return nil, fmt.Errorf("%s must be a mapping, not null (use {} for an empty policy)", name)
		}
	}
	return &cfg, nil
}
