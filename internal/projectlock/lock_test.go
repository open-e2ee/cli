package projectlock

import (
	"context"
	"testing"
	"time"
)

func TestOnlyOneWriterCanHoldProjectLock(t *testing.T) {
	directory := t.TempDir()
	first, err := Acquire(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, directory); err == nil {
		t.Fatal("second writer acquired the project lock")
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(context.Background(), directory)
	if err != nil {
		t.Fatalf("lock was not reusable: %v", err)
	}
	defer second.Release()
}
