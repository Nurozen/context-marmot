package update

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatcherConfig configures the file watcher.
type WatcherConfig struct {
	Paths          []string      // Directories to watch
	Debounce       time.Duration // Debounce interval for batching changes (default: 2s)
	PropagateDepth int           // Max depth for staleness propagation (default: 3)
}

// DefaultWatcherConfig returns sensible defaults.
func DefaultWatcherConfig() WatcherConfig {
	return WatcherConfig{
		Debounce:       2 * time.Second,
		PropagateDepth: 3,
	}
}

// Watcher watches source directories and triggers update cycles when files
// change.
type Watcher struct {
	engine   *Engine
	config   WatcherConfig
	watcher  *fsnotify.Watcher
	stopCh   chan struct{}
	doneCh   chan struct{}
	started  bool
	mu       sync.Mutex
	stopOnce sync.Once
}

// NewWatcher creates a Watcher that monitors the configured paths for
// filesystem events. Returns an error if any path cannot be watched.
func NewWatcher(engine *Engine, config WatcherConfig) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}

	for _, p := range config.Paths {
		if err := fw.Add(p); err != nil {
			_ = fw.Close()
			return nil, fmt.Errorf("watch path %q: %w", p, err)
		}
	}

	return &Watcher{
		engine:  engine,
		config:  config,
		watcher: fw,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}, nil
}

// Start begins watching for filesystem events in a background goroutine.
// Events are debounced: changes are accumulated for config.Debounce duration
// before triggering a batch update. The goroutine exits when the context is
// cancelled or Stop is called.
func (w *Watcher) Start(ctx context.Context) {
	w.mu.Lock()
	w.started = true
	w.mu.Unlock()
	go w.run(ctx)
}

func (w *Watcher) run(ctx context.Context) {
	defer close(w.doneCh)

	debounce := w.config.Debounce
	if debounce <= 0 {
		debounce = 2 * time.Second
	}

	var timer *time.Timer
	pending := false

	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-w.stopCh:
			if timer != nil {
				timer.Stop()
			}
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			// We care about writes, creates, and renames.
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			if !pending {
				pending = true
				timer = time.NewTimer(debounce)
			}
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			fmt.Fprintf(os.Stderr, "watcher error: %v\n", err)
		case <-func() <-chan time.Time {
			if timer != nil {
				return timer.C
			}
			return nil
		}():
			pending = false
			w.executeBatchUpdate(ctx)
		}
	}
}

func (w *Watcher) executeBatchUpdate(ctx context.Context) {
	result, err := w.engine.RunBatchUpdate(ctx, w.config.PropagateDepth)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update: batch update error: %v\n", err)
		return
	}
	if len(result.Changed) > 0 {
		fmt.Fprintf(os.Stderr, "update: detected %d changed, %d affected, %d reindexed, %d failed\n",
			len(result.Changed), len(result.Affected),
			len(result.Reindexed.Updated), len(result.Reindexed.Failed))
	}
}

// Stop signals the watcher goroutine to exit and waits for it to finish,
// then closes the underlying fsnotify watcher. Safe to call multiple times.
func (w *Watcher) Stop() error {
	var closeErr error
	w.stopOnce.Do(func() {
		w.mu.Lock()
		wasStarted := w.started
		w.mu.Unlock()

		close(w.stopCh)
		if wasStarted {
			<-w.doneCh
		}
		closeErr = w.watcher.Close()
	})
	return closeErr
}
