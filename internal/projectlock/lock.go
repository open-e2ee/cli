package projectlock

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/gofrs/flock"
)

type Lock struct {
	file *flock.Flock
}

func Acquire(ctx context.Context, directory string) (*Lock, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	file := flock.New(filepath.Join(directory, ".open-e2ee.lock"))
	locked, err := file.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock project: %w", err)
	}
	if !locked {
		return nil, fmt.Errorf("another OpenE2EE writer is active in %s", directory)
	}
	return &Lock{file: file}, nil
}

func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Unlock()
}
