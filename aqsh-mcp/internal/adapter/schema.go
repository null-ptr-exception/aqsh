package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// This is the public REST contract, independent of aqsh's Go internals.
type input struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Default     string   `json:"default"`
	Description string   `json:"description"`
	Pattern     string   `json:"pattern"`
	Enum        []string `json:"enum"`
	Min         *float64 `json:"min"`
	Max         *float64 `json:"max"`
	ValuesURL   bool     `json:"values_url"`
}

func (i input) defaultValue() (any, error) {
	if i.Default == "" {
		return nil, nil
	}
	switch i.Type {
	case "", "string":
		return i.Default, nil
	case "int", "float":
		return strconv.ParseFloat(i.Default, 64)
	case "bool":
		return strconv.ParseBool(i.Default)
	default:
		return nil, errors.New("unsupported input type")
	}
}

func inputSchema(inputs []input) (map[string]any, *jsonschema.Resolved, error) {
	properties := map[string]any{}
	required := []string{}
	for _, i := range inputs {
		if i.Name == "" || properties[i.Name] != nil {
			return nil, nil, errors.New("empty or duplicate input")
		}
		p := map[string]any{}
		switch i.Type {
		case "", "string":
			p["type"] = "string"
			if i.Pattern != "" {
				if _, err := regexp.Compile(i.Pattern); err != nil || strings.Contains(i.Pattern, "(?") {
					return nil, nil, errors.New("input pattern is not portable JSON Schema")
				}
				p["pattern"] = i.Pattern
			}
			if len(i.Enum) > 0 {
				p["enum"] = i.Enum
			}
		case "int":
			p["type"], p["minimum"], p["maximum"] = "integer", -9007199254740991, 9007199254740991
		case "float":
			p["type"] = "number"
		case "bool":
			p["type"] = "boolean"
		default:
			return nil, nil, fmt.Errorf("unsupported input type %q", i.Type)
		}
		if i.ValuesURL && i.Type != "" && i.Type != "string" {
			return nil, nil, errors.New("values_url requires string inputs")
		}
		if i.Min != nil {
			if i.Type != "int" || *i.Min > -9007199254740991 {
				p["minimum"] = *i.Min
			}
		}
		if i.Max != nil {
			if i.Type != "int" || *i.Max < 9007199254740991 {
				p["maximum"] = *i.Max
			}
		}
		value, err := i.defaultValue()
		if err != nil {
			return nil, nil, errors.New("invalid task default")
		}
		if i.Required && value == nil {
			required = append(required, i.Name)
		} else {
			p = map[string]any{"anyOf": []any{p, map[string]any{"type": "null"}}}
		}
		if value != nil {
			p["default"] = value
		}
		if i.Description != "" {
			p["description"] = i.Description
		}
		properties[i.Name] = p
	}
	schema := map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	resolved, err := resolveSchema(schema)
	return schema, resolved, err
}

func resolveSchema(value any) (*jsonschema.Resolved, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, err
	}
	return schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
}

func arguments(raw json.RawMessage, inputs []input, schema *jsonschema.Resolved) (map[string]any, error) {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil || args == nil {
		return nil, errors.New("arguments must be a JSON object")
	}
	// Normalize before POST so aqsh authorizes the effective target, including
	// defaults and optional blank values_url fields, before enqueueing anything.
	for _, i := range inputs {
		if args[i.Name] != nil {
			continue
		}
		value, err := i.defaultValue()
		if err != nil {
			return nil, errors.New("invalid task default")
		}
		if value != nil {
			args[i.Name] = value
		} else if i.ValuesURL && !i.Required {
			args[i.Name] = ""
		}
	}
	if err := schema.Validate(args); err != nil {
		return nil, errors.New("invalid tool arguments")
	}
	return args, nil
}
