package tasks

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

type TasksConfig struct {
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
	AllowedGroups []string `yaml:"allowed_groups"`
	Input         []Input  `yaml:"input"`
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
	AllowedGroups []string
	Input         []Input
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

	return &cfg, nil
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
		AllowedGroups: task.AllowedGroups,
		Input:         task.Input,
	}, nil
}

func (c *TasksConfig) ValidatePayload(taskName string, payload map[string]any) (map[string]string, error) {
	task, err := c.Resolve(taskName)
	if err != nil {
		return nil, err
	}

	env := make(map[string]string)

	// Check for unknown fields
	knownFields := make(map[string]bool)
	for _, input := range task.Input {
		knownFields[input.Name] = true
	}
	for k := range payload {
		if !knownFields[k] {
			return nil, fmt.Errorf("unknown field %q", k)
		}
	}

	// Validate and build env
	for _, input := range task.Input {
		value := payload[input.Name]
		if err := input.Validate(value); err != nil {
			return nil, err
		}
		if input.Env != "" {
			env[input.Env] = input.GetEnvValue(value)
		}
	}

	return env, nil
}
