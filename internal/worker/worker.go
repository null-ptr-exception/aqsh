package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/rophy/aqsh/internal/config"
	"github.com/rophy/aqsh/internal/logs"
	"github.com/rophy/aqsh/internal/tasks"
)

const TaskType = "aqsh:job"

type TaskPayload struct {
	Name      string            `json:"name"`
	CreatedAt time.Time         `json:"created_at"`
	Identity  string            `json:"identity,omitempty"`
	Groups    string            `json:"groups,omitempty"`
	Env       map[string]string `json:"env"`
	Payload   map[string]any    `json:"payload"`
}

type TaskResult struct {
	ExitCode int     `json:"exit_code"`
	Data     *string `json:"data,omitempty"`
	Error    string  `json:"error,omitempty"`
}

const maxResultSize = 1024 * 1024 // 1MB

type Worker struct {
	cfg       *config.Config
	tasks     *tasks.TasksConfig
	rdb       redis.UniversalClient
	logStream *logs.LogStreamer
	asynqOpt  asynq.RedisConnOpt
}

func New(cfg *config.Config, tasksConfig *tasks.TasksConfig, rdb redis.UniversalClient, asynqOpt asynq.RedisConnOpt) *Worker {
	return &Worker{
		cfg:       cfg,
		tasks:     tasksConfig,
		rdb:       rdb,
		logStream: logs.NewLogStreamer(rdb, cfg.LogRetention),
		asynqOpt:  asynqOpt,
	}
}

func (w *Worker) Run(ctx context.Context) error {
	queues := make(map[string]int)
	for _, q := range w.cfg.WorkerQueues {
		queues[q] = 1
	}

	srv := asynq.NewServer(w.asynqOpt, asynq.Config{
		Concurrency: w.cfg.WorkerConcurrency,
		Queues:      queues,
	})

	mux := asynq.NewServeMux()
	mux.Use(w.startedAtMiddleware)
	mux.HandleFunc(TaskType, w.handleTask)

	return srv.Run(mux)
}

const MetaKeyPrefix = "aqsh:meta:"

func (w *Worker) startedAtMiddleware(h asynq.Handler) asynq.Handler {
	return asynq.HandlerFunc(func(ctx context.Context, t *asynq.Task) error {
		taskID, _ := asynq.GetTaskID(ctx)
		metaKey := MetaKeyPrefix + taskID
		w.rdb.HSet(ctx, metaKey, "started_at", time.Now().UnixMilli())
		w.rdb.Expire(ctx, metaKey, w.cfg.ResultRetention)
		return h.ProcessTask(ctx, t)
	})
}

func (w *Worker) handleTask(ctx context.Context, task *asynq.Task) error {
	taskID, ok := asynq.GetTaskID(ctx)
	if !ok {
		return fmt.Errorf("task ID not found in context")
	}

	var payload TaskPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}

	taskDef, err := w.tasks.Resolve(payload.Name)
	if err != nil {
		return fmt.Errorf("resolve task: %w", err)
	}

	slog.Info("task started", "task_id", taskID, "task", payload.Name, "identity", payload.Identity)

	result, err := w.executeScript(ctx, task.ResultWriter(), taskID, taskDef, payload.Env, payload.Identity, payload.Groups)
	if err != nil {
		return err
	}

	slog.Info("task completed", "task_id", taskID, "task", payload.Name, "exit_code", result.ExitCode)

	resultBytes, _ := json.Marshal(result)
	if _, err := task.ResultWriter().Write(resultBytes); err != nil {
		return fmt.Errorf("write result: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("script exited with code %d", result.ExitCode)
	}

	return nil
}

func (w *Worker) executeScript(ctx context.Context, resultWriter io.Writer, taskID string, taskDef *tasks.ResolvedTask, env map[string]string, identity string, groups string) (*TaskResult, error) {
	scriptPath := taskDef.Script
	// If script is relative and doesn't contain a path separator, prepend ./
	// This ensures exec finds it in the working directory
	if !filepath.IsAbs(scriptPath) && !strings.Contains(scriptPath, string(filepath.Separator)) {
		scriptPath = "./" + scriptPath
	}

	// Create results directory and generate result file path
	// Don't create the file - let the script decide whether to write to it
	if err := os.MkdirAll(w.cfg.ResultsDir, 0755); err != nil {
		return nil, fmt.Errorf("create results dir: %w", err)
	}
	resultFilePath := filepath.Join(w.cfg.ResultsDir, "aqsh-result-"+taskID)
	defer os.Remove(resultFilePath)

	cmd := exec.CommandContext(ctx, scriptPath)
	cmd.Dir = w.cfg.TasksDir

	// Build environment
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Env = append(cmd.Env, "AQSH_TASK_ID="+taskID)
	cmd.Env = append(cmd.Env, "AQSH_RESULT_FILE="+resultFilePath)
	if identity != "" {
		cmd.Env = append(cmd.Env, "AQSH_IDENTITY="+identity)
	}
	if groups != "" {
		cmd.Env = append(cmd.Env, "AQSH_GROUPS="+groups)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start command: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)

	streamLines := func(r io.Reader, prefix string) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			_ = w.logStream.Write(ctx, taskID, prefix+line)
		}
	}

	go streamLines(stdout, "")
	go streamLines(stderr, "[stderr] ")

	wg.Wait()

	err = cmd.Wait()
	_ = w.logStream.WriteEOF(ctx, taskID)

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				exitCode = status.ExitStatus()
			} else {
				exitCode = 1
			}
		} else {
			return &TaskResult{
				ExitCode: -1,
				Error:    err.Error(),
			}, nil
		}
	}

	// Read result file if it exists and has content
	result := &TaskResult{
		ExitCode: exitCode,
	}

	if data, err := w.readResultFile(resultFilePath); err == nil && data != nil {
		result.Data = data
	}

	return result, nil
}

// readResultFile reads the result file and returns:
// - nil, nil: file doesn't exist or is empty (no result)
// - *string, nil: file has content
// - nil, error: file too large or read error
func (w *Worker) readResultFile(path string) (*string, error) {
	info, err := os.Stat(path)
	if err != nil {
		// File doesn't exist = no result (not an error)
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	if info.Size() == 0 {
		// Empty file = empty result (distinct from no result)
		empty := ""
		return &empty, nil
	}

	if info.Size() > maxResultSize {
		return nil, fmt.Errorf("result file too large: %d bytes (max %d)", info.Size(), maxResultSize)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	result := string(data)
	return &result, nil
}
