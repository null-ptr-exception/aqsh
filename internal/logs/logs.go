package logs

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	streamPrefix = "aqsh:logs:"
)

type LogStreamer struct {
	rdb       redis.UniversalClient
	retention time.Duration
}

func NewLogStreamer(rdb redis.UniversalClient, retention time.Duration) *LogStreamer {
	return &LogStreamer{
		rdb:       rdb,
		retention: retention,
	}
}

func (l *LogStreamer) streamKey(taskID string) string {
	return streamPrefix + taskID
}

func (l *LogStreamer) Write(ctx context.Context, taskID, line string) error {
	return l.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: l.streamKey(taskID),
		Values: map[string]any{"line": line},
	}).Err()
}

func (l *LogStreamer) WriteEOF(ctx context.Context, taskID string) error {
	key := l.streamKey(taskID)
	if err := l.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: key,
		Values: map[string]any{"eof": "true"},
	}).Err(); err != nil {
		return err
	}
	return l.rdb.Expire(ctx, key, l.retention).Err()
}

type LogEntry struct {
	ID   string
	Line string
	EOF  bool
}

func (l *LogStreamer) Read(ctx context.Context, taskID string, lastID string, block time.Duration) ([]LogEntry, error) {
	if lastID == "" {
		lastID = "0"
	}

	streams, err := l.rdb.XRead(ctx, &redis.XReadArgs{
		Streams: []string{l.streamKey(taskID), lastID},
		Block:   block,
		Count:   100,
	}).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading log stream: %w", err)
	}

	var entries []LogEntry
	for _, stream := range streams {
		for _, msg := range stream.Messages {
			entry := LogEntry{ID: msg.ID}
			if line, ok := msg.Values["line"].(string); ok {
				entry.Line = line
			}
			if eof, ok := msg.Values["eof"].(string); ok && eof == "true" {
				entry.EOF = true
			}
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func (l *LogStreamer) ReadAll(ctx context.Context, taskID string) ([]LogEntry, error) {
	msgs, err := l.rdb.XRange(ctx, l.streamKey(taskID), "-", "+").Result()
	if err != nil {
		return nil, fmt.Errorf("reading all logs: %w", err)
	}

	var entries []LogEntry
	for _, msg := range msgs {
		entry := LogEntry{ID: msg.ID}
		if line, ok := msg.Values["line"].(string); ok {
			entry.Line = line
		}
		if eof, ok := msg.Values["eof"].(string); ok && eof == "true" {
			entry.EOF = true
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
