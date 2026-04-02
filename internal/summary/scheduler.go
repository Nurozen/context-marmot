package summary

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/nurozen/context-marmot/internal/node"
)

// SchedulerConfig configures the background scheduler.
type SchedulerConfig struct {
	Interval       time.Duration // Periodic regen interval (0 = disabled)
	DeltaThreshold float64       // Fraction of node count change to trigger regen (e.g. 0.2 = 20%)
	MinNodes       int           // Minimum node count before generating summaries
}

// DefaultSchedulerConfig returns sensible defaults.
func DefaultSchedulerConfig() SchedulerConfig {
	return SchedulerConfig{
		Interval:       30 * time.Minute,
		DeltaThreshold: 0.2,
		MinNodes:       3,
	}
}

// Scheduler manages async summary regeneration.
type Scheduler struct {
	engine     *Engine
	config     SchedulerConfig
	dir        string
	namespace  string
	nodeLoader func() ([]*node.Node, error) // function to load current nodes

	mu            sync.Mutex
	lastNodeCount int
	lastGenerated time.Time
	running       bool
	regenerating  bool // true while a NotifyChange-spawned regeneration is in-flight
	stopCh        chan struct{}
	doneCh        chan struct{}
	wg            sync.WaitGroup // tracks NotifyChange-spawned goroutines
}

// NewScheduler creates a new Scheduler. The nodeLoader function is called to
// fetch the current set of nodes whenever the scheduler decides to regenerate.
func NewScheduler(engine *Engine, config SchedulerConfig, dir string, namespace string, nodeLoader func() ([]*node.Node, error)) *Scheduler {
	return &Scheduler{
		engine:     engine,
		config:     config,
		dir:        dir,
		namespace:  namespace,
		nodeLoader: nodeLoader,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
}

// Start begins the background regeneration goroutine.
// It ticks at config.Interval (if > 0) and regenerates when appropriate.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	s.mu.Unlock()

	go s.run(ctx)
}

// Stop signals the background goroutine to stop and blocks until it exits.
// It also waits for any in-flight NotifyChange regenerations to complete.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	close(s.stopCh)
	<-s.doneCh
	s.wg.Wait() // drain any NotifyChange-spawned goroutines

	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
}

// NotifyChange is called after writes to check whether the node count delta
// exceeds the configured threshold. If so, it triggers an async regeneration.
// Only one NotifyChange-spawned regeneration runs at a time to avoid duplicate work.
func (s *Scheduler) NotifyChange(currentNodeCount int) {
	s.mu.Lock()
	if s.regenerating {
		s.mu.Unlock()
		return // another regeneration is already in-flight
	}
	shouldRegen := s.shouldRegenerate(currentNodeCount)
	if shouldRegen {
		s.regenerating = true
	}
	s.mu.Unlock()

	if shouldRegen {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				s.regenerating = false
				s.mu.Unlock()
			}()
			s.regenerate()
		}()
	}
}

// shouldRegenerate checks delta + interval + minNodes. Must be called with mu held.
func (s *Scheduler) shouldRegenerate(currentCount int) bool {
	if currentCount < s.config.MinNodes {
		return false
	}

	// Check node count delta.
	if s.lastNodeCount > 0 {
		delta := float64(abs(currentCount-s.lastNodeCount)) / float64(s.lastNodeCount)
		if delta >= s.config.DeltaThreshold {
			return true
		}
	} else if currentCount >= s.config.MinNodes {
		// First time: no previous count, threshold met.
		return true
	}

	// Check time-based interval.
	if s.config.Interval > 0 && !s.lastGenerated.IsZero() {
		if time.Since(s.lastGenerated) >= s.config.Interval {
			return true
		}
	}

	return false
}

func (s *Scheduler) run(ctx context.Context) {
	defer close(s.doneCh)

	if s.config.Interval <= 0 {
		// No periodic regeneration; just wait for stop.
		select {
		case <-s.stopCh:
		case <-ctx.Done():
		}
		return
	}

	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.regenerate()
		}
	}
}

func (s *Scheduler) regenerate() {
	nodes, err := s.nodeLoader()
	if err != nil {
		log.Printf("[summary] scheduler: load nodes for %s: %v", s.namespace, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := s.engine.GenerateSummary(ctx, s.namespace, nodes)
	if err != nil {
		log.Printf("[summary] scheduler: generate for %s: %v", s.namespace, err)
		return
	}

	if err := WriteSummary(s.dir, s.namespace, result); err != nil {
		log.Printf("[summary] scheduler: write for %s: %v", s.namespace, err)
		return
	}

	s.mu.Lock()
	s.lastNodeCount = result.NodeCount
	s.lastGenerated = result.GeneratedAt
	s.mu.Unlock()

	log.Printf("[summary] scheduler: regenerated summary for %s (%d nodes)", s.namespace, result.NodeCount)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
