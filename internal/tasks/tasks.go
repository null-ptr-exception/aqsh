package tasks

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type TasksConfig struct {
	Include  []string           `yaml:"include"`
	Defaults TaskDefaults       `yaml:"defaults"`
	Tasks    map[string]TaskDef `yaml:"tasks"`
}

type TaskDefaults struct {
	Timeout      string `yaml:"timeout"`
	MaxRetry     int    `yaml:"max_retry"`
	RetryDelay   string `yaml:"retry_delay"`
	Queue        string `yaml:"queue"`
	LogRetention string `yaml:"log_retention"`
}

type TaskDef struct {
	Script        string   `yaml:"script"`
	Description   string   `yaml:"description"`
	Timeout       string   `yaml:"timeout"`
	MaxRetry      *int     `yaml:"max_retry"`
	RetryDelay    string   `yaml:"retry_delay"`
	Queue         string   `yaml:"queue"`
	AllowedUsers  []string `yaml:"allowed_users"`
	AllowedGroups []string `yaml:"allowed_groups"`
	Input         []Input  `yaml:"input"`
	MCP           *MCPTool `yaml:"mcp"`
}

type Input struct {
	Name        string   `yaml:"name"`
	Env         string   `yaml:"env"`
	Required    bool     `yaml:"required"`
	Type        string   `yaml:"type"` // string, int, float, bool
	Pattern     string   `yaml:"pattern"`
	Enum        []string `yaml:"enum"`
	Min         *float64 `yaml:"min"`
	Max         *float64 `yaml:"max"`
	Default     string   `yaml:"default"`
	Description string   `yaml:"description"`
	ValuesURL   string   `yaml:"values_url"`
	ValuesCache string   `yaml:"values_cache"`

	compiledPattern *regexp.Regexp
}

func (i *Input) Validate(value any) error {
	if value == nil {
		if i.Required && i.Default == "" {
			return fmt.Errorf("field %q is required", i.Name)
		}
		return nil
	}

	switch i.Type {
	case "string", "":
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("field %q must be a string", i.Name)
		}
		if i.Pattern != "" {
			if i.compiledPattern == nil {
				var err error
				i.compiledPattern, err = regexp.Compile(i.Pattern)
				if err != nil {
					return fmt.Errorf("invalid pattern for field %q: %v", i.Name, err)
				}
			}
			if !i.compiledPattern.MatchString(s) {
				return fmt.Errorf("field %q does not match pattern %q", i.Name, i.Pattern)
			}
		}
		if len(i.Enum) > 0 {
			found := false
			for _, e := range i.Enum {
				if s == e {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("field %q must be one of %v", i.Name, i.Enum)
			}
		}

	case "int":
		var n float64
		switch v := value.(type) {
		case float64:
			n = v
		case int:
			n = float64(v)
		default:
			return fmt.Errorf("field %q must be an integer", i.Name)
		}
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < -0x1p63 || n >= 0x1p63 {
			return fmt.Errorf("field %q must be an integer within the int64 range", i.Name)
		}
		if i.Min != nil && n < *i.Min {
			return fmt.Errorf("field %q must be >= %v", i.Name, *i.Min)
		}
		if i.Max != nil && n > *i.Max {
			return fmt.Errorf("field %q must be <= %v", i.Name, *i.Max)
		}

	case "float":
		var n float64
		switch v := value.(type) {
		case float64:
			n = v
		case int:
			n = float64(v)
		default:
			return fmt.Errorf("field %q must be a number", i.Name)
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return fmt.Errorf("field %q must be finite", i.Name)
		}
		if i.Min != nil && n < *i.Min {
			return fmt.Errorf("field %q must be >= %v", i.Name, *i.Min)
		}
		if i.Max != nil && n > *i.Max {
			return fmt.Errorf("field %q must be <= %v", i.Name, *i.Max)
		}

	case "bool":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("field %q must be a boolean", i.Name)
		}
	default:
		return fmt.Errorf("field %q has unsupported type %q", i.Name, i.Type)
	}

	return nil
}

func (i *Input) GetEnvValue(value any) string {
	if value == nil {
		return i.Default
	}
	switch v := value.(type) {
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		if i.Type == "int" {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

type ResolvedTask struct {
	Name          string
	Script        string
	Description   string
	Timeout       time.Duration
	MaxRetry      int
	RetryDelay    time.Duration
	Queue         string
	AllowedUsers  []string
	AllowedGroups []string
	Input         []Input
	MCP           *MCPTool
}

func Load(path string) (*TasksConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading tasks config: %w", err)
	}

	var cfg TasksConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing tasks config: %w", err)
	}

	if cfg.Tasks == nil {
		cfg.Tasks = make(map[string]TaskDef)
	}

	sourceFile := filepath.Base(path)
	taskSources := make(map[string]string)
	for taskName := range cfg.Tasks {
		taskSources[taskName] = sourceFile
	}

	if err := validateInputs(cfg.Tasks); err != nil {
		return nil, err
	}

	baseDir := filepath.Dir(path)
	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolving base directory: %w", err)
	}
	loaded := make(map[string]bool)
	for _, pattern := range cfg.Include {
		if filepath.IsAbs(pattern) {
			return nil, fmt.Errorf("include pattern %q must be relative", pattern)
		}
		resolved := filepath.Clean(filepath.Join(absBaseDir, pattern))
		if rel, err := filepath.Rel(absBaseDir, resolved); err != nil || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("include pattern %q escapes base directory", pattern)
		}
		matches, err := filepath.Glob(resolved)
		if err != nil {
			return nil, fmt.Errorf("invalid include pattern %q: %w", pattern, err)
		}
		for _, match := range matches {
			absMatch, err := filepath.Abs(match)
			if err != nil {
				return nil, fmt.Errorf("resolving include file %q: %w", match, err)
			}
			if loaded[absMatch] {
				continue
			}
			loaded[absMatch] = true
			if err := loadInclude(&cfg, absMatch, taskSources); err != nil {
				return nil, err
			}
		}
	}

	if err := cfg.ValidateMCP(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func loadInclude(cfg *TasksConfig, path string, taskSources map[string]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading included file %q: %w", path, err)
	}

	var included struct {
		Include  []string           `yaml:"include"`
		Defaults TaskDefaults       `yaml:"defaults"`
		Tasks    map[string]TaskDef `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(data, &included); err != nil {
		return fmt.Errorf("parsing included file %q: %w", path, err)
	}
	if len(included.Include) > 0 {
		return fmt.Errorf("included file %q must not define include", path)
	}
	if included.Defaults != (TaskDefaults{}) {
		return fmt.Errorf("included file %q must not define defaults", path)
	}

	if err := validateInputs(included.Tasks); err != nil {
		return err
	}

	source := filepath.Base(path)
	for taskName, taskDef := range included.Tasks {
		if existingSource, exists := taskSources[taskName]; exists {
			return fmt.Errorf("task %q defined in both %s and %s", taskName, existingSource, source)
		}
		taskSources[taskName] = source
		cfg.Tasks[taskName] = taskDef
	}

	return nil
}

func validateInputs(tasks map[string]TaskDef) error {
	for taskName, task := range tasks {
		for _, input := range task.Input {
			if input.ValuesURL != "" && len(input.Enum) > 0 {
				return fmt.Errorf("task %q input %q: values_url and enum are mutually exclusive", taskName, input.Name)
			}
			if input.ValuesCache != "" && input.ValuesURL == "" {
				return fmt.Errorf("task %q input %q: values_cache requires values_url", taskName, input.Name)
			}
			if input.ValuesCache != "" {
				if _, err := time.ParseDuration(input.ValuesCache); err != nil {
					return fmt.Errorf("task %q input %q: invalid values_cache %q: %w", taskName, input.Name, input.ValuesCache, err)
				}
			}
		}
	}
	return nil
}

func (c *TasksConfig) Resolve(name string) (*ResolvedTask, error) {
	task, ok := c.Tasks[name]
	if !ok {
		return nil, fmt.Errorf("task %q not found", name)
	}

	timeout := c.Defaults.Timeout
	if task.Timeout != "" {
		timeout = task.Timeout
	}
	if timeout == "" {
		timeout = "5m"
	}
	timeoutDur, err := time.ParseDuration(timeout)
	if err != nil {
		return nil, fmt.Errorf("invalid timeout %q: %w", timeout, err)
	}

	retryDelay := c.Defaults.RetryDelay
	if task.RetryDelay != "" {
		retryDelay = task.RetryDelay
	}
	if retryDelay == "" {
		retryDelay = "30s"
	}
	retryDelayDur, err := time.ParseDuration(retryDelay)
	if err != nil {
		return nil, fmt.Errorf("invalid retry_delay %q: %w", retryDelay, err)
	}

	maxRetry := c.Defaults.MaxRetry
	if task.MaxRetry != nil {
		maxRetry = *task.MaxRetry
	}

	queue := c.Defaults.Queue
	if task.Queue != "" {
		queue = task.Queue
	}
	if queue == "" {
		queue = "default"
	}

	return &ResolvedTask{
		Name:          name,
		Script:        task.Script,
		Description:   task.Description,
		Timeout:       timeoutDur,
		MaxRetry:      maxRetry,
		RetryDelay:    retryDelayDur,
		Queue:         queue,
		AllowedUsers:  task.AllowedUsers,
		AllowedGroups: task.AllowedGroups,
		Input:         task.Input,
		MCP:           task.MCP,
	}, nil
}

func (c *TasksConfig) ValidatePayload(taskName string, payload map[string]any) (map[string]string, error) {
	payload, err := c.NormalizePayload(taskName, payload)
	if err != nil {
		return nil, err
	}
	task, err := c.Resolve(taskName)
	if err != nil {
		return nil, err
	}

	env := make(map[string]string)

	// Normalization already validated fields and effective defaults.
	for _, input := range task.Input {
		value := payload[input.Name]
		if input.Env != "" {
			env[input.Env] = input.GetEnvValue(value)
		}
	}

	return env, nil
}
