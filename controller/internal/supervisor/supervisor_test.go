package supervisor_test

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/imdomestic/chorus-testbed/controller/internal/events"
	"github.com/imdomestic/chorus-testbed/controller/internal/model"
	"github.com/imdomestic/chorus-testbed/controller/internal/store"
	"github.com/imdomestic/chorus-testbed/controller/internal/supervisor"
)

type synchronizedLog struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (l *synchronizedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buffer.Write(p)
}

func (l *synchronizedLog) contains(value string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return bytes.Contains(l.buffer.Bytes(), []byte(value))
}

func TestDelayCapsAtOneMinute(t *testing.T) {
	for failures, want := range map[int]time.Duration{0: 0, 1: time.Second, 2: 2 * time.Second, 6: 32 * time.Second, 7: time.Minute, 1000: time.Minute} {
		if got := supervisor.Delay(failures); got != want {
			t.Fatalf("failures=%d got=%s want=%s", failures, got, want)
		}
	}
}

func TestRealEventBusRecoversAfterSQLiteRecordRepair(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bus := events.New(db)
	_, stream, closeSub, err := bus.Subscribe(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer closeSub()
	if err = db.Put(ctx, "room", "broken-record", 1); err != nil {
		t.Fatal(err)
	}
	output := &synchronizedLog{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(output, nil)))
	defer slog.SetDefault(previous)
	done := make(chan struct{})
	go func() {
		supervisor.Run(ctx, "events", bus.Run)
		close(done)
	}()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !output.contains("retry scheduled") {
		select {
		case <-ctx.Done():
			t.Fatal("event failure was not reported")
		case <-ticker.C:
		}
	}
	if err = db.Delete(ctx, "room", "broken-record"); err != nil {
		t.Fatal(err)
	}
	room := model.Room{ID: "recovered", State: "stopped"}
	if err = db.Put(ctx, "room", room.ID, room); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-stream:
		if event.Name != "room" || event.RoomID != room.ID {
			t.Fatalf("event: %+v", event)
		}
	case <-ctx.Done():
		t.Fatal("event loop did not resume")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not stop")
	}
}
