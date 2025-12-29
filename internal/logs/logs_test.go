package logs

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestNewLogStreamer(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	retention := 24 * time.Hour
	ls := NewLogStreamer(rdb, retention)

	if ls == nil {
		t.Fatal("expected non-nil LogStreamer")
	}
	if ls.retention != retention {
		t.Errorf("expected retention=%v, got %v", retention, ls.retention)
	}
}

func TestStreamKey(t *testing.T) {
	ls := &LogStreamer{}

	key := ls.streamKey("task-123")
	expected := "aqsh:logs:task-123"
	if key != expected {
		t.Errorf("expected %q, got %q", expected, key)
	}
}

func TestWriteAndRead(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	ls := NewLogStreamer(rdb, time.Hour)
	ctx := context.Background()
	taskID := "test-task-1"

	// Write some log lines
	if err := ls.Write(ctx, taskID, "line 1"); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := ls.Write(ctx, taskID, "line 2"); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Read with short timeout (Block: 0 means infinite in Redis, so use small duration)
	entries, err := ls.Read(ctx, taskID, "0", time.Millisecond)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	if entries[0].Line != "line 1" {
		t.Errorf("expected 'line 1', got %q", entries[0].Line)
	}
	if entries[1].Line != "line 2" {
		t.Errorf("expected 'line 2', got %q", entries[1].Line)
	}
	if entries[0].EOF || entries[1].EOF {
		t.Error("expected EOF=false for log lines")
	}
}

func TestWriteEOF(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	ls := NewLogStreamer(rdb, time.Hour)
	ctx := context.Background()
	taskID := "test-task-2"

	// Write a log line and then EOF
	if err := ls.Write(ctx, taskID, "final line"); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := ls.WriteEOF(ctx, taskID); err != nil {
		t.Fatalf("WriteEOF failed: %v", err)
	}

	entries, err := ls.Read(ctx, taskID, "0", time.Millisecond)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	if entries[0].Line != "final line" {
		t.Errorf("expected 'final line', got %q", entries[0].Line)
	}
	if entries[0].EOF {
		t.Error("expected first entry to not be EOF")
	}

	if !entries[1].EOF {
		t.Error("expected second entry to be EOF")
	}
}

func TestReadWithLastID(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	ls := NewLogStreamer(rdb, time.Hour)
	ctx := context.Background()
	taskID := "test-task-3"

	// Write some log lines
	ls.Write(ctx, taskID, "line 1")
	ls.Write(ctx, taskID, "line 2")
	ls.Write(ctx, taskID, "line 3")

	// Read all entries first
	entries, err := ls.Read(ctx, taskID, "0", time.Millisecond)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	// Read from the ID of the first entry
	laterEntries, err := ls.Read(ctx, taskID, entries[0].ID, time.Millisecond)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if len(laterEntries) != 2 {
		t.Fatalf("expected 2 entries after first, got %d", len(laterEntries))
	}

	if laterEntries[0].Line != "line 2" {
		t.Errorf("expected 'line 2', got %q", laterEntries[0].Line)
	}
}

func TestReadEmptyStream(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	ls := NewLogStreamer(rdb, time.Hour)
	ctx := context.Background()

	// Read from non-existent stream with short timeout
	entries, err := ls.Read(ctx, "nonexistent-task", "", time.Millisecond)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if entries != nil && len(entries) != 0 {
		t.Errorf("expected nil or empty entries for non-existent stream, got %v", entries)
	}
}

func TestReadAll(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	ls := NewLogStreamer(rdb, time.Hour)
	ctx := context.Background()
	taskID := "test-task-4"

	// Write multiple log lines
	ls.Write(ctx, taskID, "line 1")
	ls.Write(ctx, taskID, "line 2")
	ls.Write(ctx, taskID, "line 3")
	ls.WriteEOF(ctx, taskID)

	entries, err := ls.ReadAll(ctx, taskID)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if len(entries) != 4 {
		t.Fatalf("expected 4 entries (3 lines + EOF), got %d", len(entries))
	}

	for i, line := range []string{"line 1", "line 2", "line 3"} {
		if entries[i].Line != line {
			t.Errorf("entry %d: expected %q, got %q", i, line, entries[i].Line)
		}
		if entries[i].EOF {
			t.Errorf("entry %d: expected EOF=false", i)
		}
	}

	if !entries[3].EOF {
		t.Error("expected last entry to be EOF")
	}
}

func TestReadAllEmptyStream(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	ls := NewLogStreamer(rdb, time.Hour)
	ctx := context.Background()

	entries, err := ls.ReadAll(ctx, "nonexistent-task")
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("expected empty entries for non-existent stream, got %d", len(entries))
	}
}

func TestLogEntryIDs(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	ls := NewLogStreamer(rdb, time.Hour)
	ctx := context.Background()
	taskID := "test-task-5"

	ls.Write(ctx, taskID, "line 1")
	ls.Write(ctx, taskID, "line 2")

	entries, _ := ls.Read(ctx, taskID, "0", time.Millisecond)

	// Each entry should have a non-empty ID
	for i, entry := range entries {
		if entry.ID == "" {
			t.Errorf("entry %d: expected non-empty ID", i)
		}
	}

	// IDs should be unique and ordered
	if entries[0].ID == entries[1].ID {
		t.Error("expected unique IDs for different entries")
	}
}

func TestWriteEOFSetsExpiration(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	retention := 2 * time.Hour
	ls := NewLogStreamer(rdb, retention)
	ctx := context.Background()
	taskID := "test-task-6"

	ls.Write(ctx, taskID, "line 1")
	ls.WriteEOF(ctx, taskID)

	// Check that the key has a TTL set
	ttl := mr.TTL(ls.streamKey(taskID))
	if ttl <= 0 {
		t.Errorf("expected positive TTL after WriteEOF, got %v", ttl)
	}
	if ttl > retention {
		t.Errorf("expected TTL <= %v, got %v", retention, ttl)
	}
}
