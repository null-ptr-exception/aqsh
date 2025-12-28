package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/rophy/aqsh/internal/config"
	"github.com/rophy/aqsh/internal/hooks"
	"github.com/rophy/aqsh/internal/logs"
)

const TaskType = "aqsh:job"

type TaskPayload struct {
	Hook    string            `json:"hook"`
	Env     map[string]string `json:"env"`
	Payload map[string]any    `json:"payload"`
}

type TaskResult struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
	Error    string `json:"error,omitempty"`
}

type Worker struct {
	cfg        *config.Config
	hooks      *hooks.HooksConfig
	logStream  *logs.LogStreamer
	asynqOpt   asynq.RedisConnOpt
}

func New(cfg *config.Config, hooksConfig *hooks.HooksConfig, rdb redis.UniversalClient, asynqOpt asynq.RedisConnOpt) *Worker {
	return &Worker{
		cfg:       cfg,
		hooks:     hooksConfig,
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
	mux.HandleFunc(TaskType, w.handleTask)

	return srv.Run(mux)
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

	hook, err := w.hooks.Resolve(payload.Hook)
	if err != nil {
		return fmt.Errorf("resolve hook: %w", err)
	}

	result, err := w.executeScript(ctx, task.ResultWriter(), taskID, hook, payload.Env)
	if err != nil {
		return err
	}

	resultBytes, _ := json.Marshal(result)
	if _, err := task.ResultWriter().Write(resultBytes); err != nil {
		return fmt.Errorf("write result: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("script exited with code %d", result.ExitCode)
	}

	return nil
}

func (w *Worker) executeScript(ctx context.Context, resultWriter io.Writer, taskID string, hook *hooks.ResolvedHook, env map[string]string) (*TaskResult, error) {
	scriptPath := hook.Script
	// If script is relative and doesn't contain a path separator, prepend ./
	// This ensures exec finds it in the working directory
	if !filepath.IsAbs(scriptPath) && !strings.Contains(scriptPath, string(filepath.Separator)) {
		scriptPath = "./" + scriptPath
	}

	cmd := exec.CommandContext(ctx, scriptPath)
	cmd.Dir = w.cfg.ScriptsDir

	// Build environment
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Env = append(cmd.Env, "AQSH_TASK_ID="+taskID)

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

	var output strings.Builder
	var wg sync.WaitGroup
	wg.Add(2)

	streamLines := func(r io.Reader, prefix string) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			output.WriteString(line)
			output.WriteString("\n")
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
				Output:   output.String(),
				Error:    err.Error(),
			}, nil
		}
	}

	return &TaskResult{
		ExitCode: exitCode,
		Output:   output.String(),
	}, nil
}
