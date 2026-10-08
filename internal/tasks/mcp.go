package tasks

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// MCPTool is an explicit opt-in. Classification and JSON output must be reviewed
// by the project that owns the script; metadata does not authorize a caller.
type MCPTool struct {
	Enabled        bool   `yaml:"enabled"`
	Name           string `yaml:"name"`
	Description    string `yaml:"description"`
	ReadOnly       bool   `yaml:"read_only"`
	ResultMode     string `yaml:"result_mode"` // omitted (default), json
	MaxResultBytes int    `yaml:"max_result_bytes"`
}

func (m *MCPTool) ResultLimit() int {
	if m.MaxResultBytes > 0 {
		return m.MaxResultBytes
	}
	return 64 * 1024
}

var mcpNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func (c *TasksConfig) ValidateMCP() error {
	names := make(map[string]string)
	taskNames := make([]string, 0, len(c.Tasks))
	for name := range c.Tasks {
		taskNames = append(taskNames, name)
	}
	sort.Strings(taskNames)
	for _, name := range taskNames {
		task := c.Tasks[name]
		m := task.MCP
		if m == nil || !m.Enabled {
			continue
		}
		if !mcpNamePattern.MatchString(m.Name) || strings.HasPrefix(m.Name, "aqsh_") {
			return fmt.Errorf("task %q: invalid or reserved MCP name %q", name, m.Name)
		}
		if previous, ok := names[m.Name]; ok {
			return fmt.Errorf("tasks %q and %q share MCP name %q", previous, name, m.Name)
		}
		names[m.Name] = name
		if !m.ReadOnly {
			return fmt.Errorf("task %q: MCP currently requires read_only: true", name)
		}
		if m.ResultMode != "" && m.ResultMode != "omitted" && m.ResultMode != "json" {
			return fmt.Errorf("task %q: MCP result_mode must be omitted or json", name)
		}
		if m.MaxResultBytes < 0 || m.MaxResultBytes > 1024*1024 {
			return fmt.Errorf("task %q: MCP max_result_bytes must be between 0 and 1048576", name)
		}
		if _, err := c.MCPInputSchema(name); err != nil {
			return err
		}
	}
	return nil
}

// DefaultValue parses the string configuration without changing string-valued flags.
func (i Input) DefaultValue() (any, error) {
	if i.Default == "" {
		return nil, nil
	}
	var value any
	var err error
	switch i.Type {
	case "", "string":
		value = i.Default
	case "int", "float":
		value, err = strconv.ParseFloat(i.Default, 64)
	case "bool":
		value, err = strconv.ParseBool(i.Default)
	default:
		err = fmt.Errorf("unsupported type %q", i.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("field %q has an invalid default", i.Name)
	}
	if err := i.Validate(value); err != nil {
		return nil, fmt.Errorf("invalid default: %w", err)
	}
	return value, nil
}

// NormalizePayload makes effective defaults visible to both validation and target
// authorization. It returns a copy so callers cannot mutate stored arguments.
func (c *TasksConfig) NormalizePayload(name string, payload map[string]any) (map[string]any, error) {
	task, err := c.Resolve(name)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(task.Input))
	for _, input := range task.Input {
		known[input.Name] = true
	}
	for key := range payload {
		if !known[key] {
			return nil, fmt.Errorf("unknown field %q", key)
		}
	}
	normalized := make(map[string]any, len(task.Input))
	for _, input := range task.Input {
		value := payload[input.Name]
		if value == nil {
			value, err = input.DefaultValue()
			if err != nil {
				return nil, err
			}
		}
		if err := input.Validate(value); err != nil {
			return nil, err
		}
		if value != nil {
			normalized[input.Name] = value
		}
	}
	return normalized, nil
}

// MCPInputSchema exposes the task contract without env names or policy URLs.
func (c *TasksConfig) MCPInputSchema(name string) (map[string]any, error) {
	task, err := c.Resolve(name)
	if err != nil {
		return nil, err
	}
	properties := make(map[string]any, len(task.Input))
	required := make([]string, 0)
	for _, input := range task.Input {
		if input.Name == "" || properties[input.Name] != nil {
			return nil, fmt.Errorf("task %q has empty or duplicate input names", name)
		}
		property := make(map[string]any)
		switch input.Type {
		case "", "string":
			property["type"] = "string"
			if input.Pattern != "" {
				if _, err := regexp.Compile(input.Pattern); err != nil {
					return nil, fmt.Errorf("task %q input %q: invalid pattern", name, input.Name)
				}
				// Go flags and named groups are not portable JSON Schema regexes.
				if strings.Contains(input.Pattern, "(?") {
					return nil, fmt.Errorf("task %q input %q: MCP requires a portable pattern", name, input.Name)
				}
				property["pattern"] = input.Pattern
			}
			if len(input.Enum) > 0 {
				property["enum"] = input.Enum
			}
		case "int":
			property["type"] = "integer"
			property["minimum"] = -0x1p63
			property["exclusiveMaximum"] = 0x1p63
		case "float":
			property["type"] = "number"
		case "bool":
			property["type"] = "boolean"
		default:
			return nil, fmt.Errorf("task %q input %q: unsupported type %q", name, input.Name, input.Type)
		}
		if input.Min != nil && (input.Type != "int" || *input.Min > -0x1p63) {
			property["minimum"] = *input.Min
		}
		if input.Max != nil {
			property["maximum"] = *input.Max
		}
		if input.ValuesURL != "" && input.Type != "" && input.Type != "string" {
			return nil, fmt.Errorf("task %q input %q: values_url requires a string", name, input.Name)
		}
		value, err := input.DefaultValue()
		if err != nil {
			return nil, fmt.Errorf("task %q: %w", name, err)
		}
		if input.Required && value == nil {
			required = append(required, input.Name)
		} else {
			property = map[string]any{"anyOf": []any{property, map[string]any{"type": "null"}}}
		}
		if value != nil {
			property["default"] = value
		}
		if input.Description != "" {
			property["description"] = input.Description
		}
		properties[input.Name] = property
	}
	schema := map[string]any{
		"type": "object", "properties": properties,
		"required": required, "additionalProperties": false,
	}
	if _, err := json.Marshal(schema); err != nil {
		return nil, fmt.Errorf("task %q has an invalid MCP schema: %w", name, err)
	}
	return schema, nil
}
