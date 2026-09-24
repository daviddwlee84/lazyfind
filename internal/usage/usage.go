// Package usage measures directory disk usage on demand. Measurements describe
// allocated storage (du -k), include hidden/ignored children, and never enter
// search history or the persistent preview cache.
package usage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazyfind/internal/config"
	"github.com/daviddwlee84/lazyfind/internal/domain"
	"github.com/daviddwlee84/lazyfind/internal/transport"
)

type Result struct {
	ItemID     string    `json:"item_id"`
	Bytes      int64     `json:"bytes"`
	Status     string    `json:"status"`
	MeasuredAt time.Time `json:"measured_at"`
	Error      string    `json:"error,omitempty"`
	Cached     bool      `json:"cached"`
}

type commandRunner interface {
	Run(context.Context, domain.Target, []string, string) ([]byte, error)
}

type Service struct {
	runner      commandRunner
	concurrency int
	timeout     time.Duration
	slots       chan struct{}
	mu          sync.Mutex
	cache       map[string]Result
	epochs      map[string]uint64
}

func New(cfg config.Config) *Service {
	concurrency := cfg.DirectoryUsage.Concurrency
	if concurrency < 1 {
		concurrency = 2
	}
	timeout := cfg.DirectoryUsage.TimeoutSeconds
	if timeout < 1 {
		timeout = 60
	}
	return &Service{runner: transport.New(cfg), concurrency: concurrency, timeout: time.Duration(timeout) * time.Second, slots: make(chan struct{}, concurrency), cache: make(map[string]Result), epochs: make(map[string]uint64)}
}

// Measure returns one measurement. Complete results remain in this Service's
// memory until force refresh or Service disposal. A forced refresh invalidates
// the old entry even when the new attempt fails.
func (s *Service) Measure(ctx context.Context, item domain.Item, force bool) Result {
	key := domain.ItemID(item.Target, item.RawPath())
	result := Result{ItemID: item.ID}
	if result.ItemID == "" {
		result.ItemID = key
	}
	finish := func(status string, err error) Result {
		result.Status = status
		result.MeasuredAt = time.Now()
		if err != nil {
			result.Error = domain.Display(err.Error())
		}
		return result
	}
	if item.Kind != "directory" && item.Kind != "dir" {
		return finish("failed", errors.New("disk usage is only available for directories"))
	}
	if item.RawPath() == "" {
		return finish("failed", errors.New("directory path is empty"))
	}
	if err := ctx.Err(); err != nil {
		return finish(contextStatus(err), err)
	}
	s.mu.Lock()
	if force {
		delete(s.cache, key)
		s.epochs[key]++
	} else if cached, ok := s.cache[key]; ok {
		s.mu.Unlock()
		cached.ItemID = result.ItemID
		cached.Cached = true
		return cached
	}
	epoch := s.epochs[key]
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return finish(contextStatus(ctx.Err()), ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		return finish(contextStatus(err), err)
	}
	// A previous waiter may have completed this path while this call queued.
	if !force {
		s.mu.Lock()
		cached, ok := s.cache[key]
		s.mu.Unlock()
		if ok {
			cached.ItemID = result.ItemID
			cached.Cached = true
			return cached
		}
	}
	output, runErr := s.runner.Run(ctx, item.Target, []string{"env", "LC_ALL=C", "du", "-skP", "."}, item.RawPath())
	bytes, parseErr := parseKiB(output)
	if parseErr == nil {
		result.Bytes = bytes
	}
	if err := ctx.Err(); err != nil {
		return finish(contextStatus(err), err)
	}
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return finish(contextStatus(runErr), runErr)
	}
	if runErr != nil {
		if parseErr == nil {
			return finish("partial", runErr)
		}
		return finish("failed", fmt.Errorf("%v; %w", runErr, parseErr))
	}
	if parseErr != nil {
		return finish("failed", parseErr)
	}
	result = finish("complete", nil)
	s.mu.Lock()
	if s.epochs[key] == epoch {
		s.cache[key] = result
	}
	s.mu.Unlock()
	return result
}

// MeasureBatch starts at most the configured number of workers and emits on the
// caller goroutine, so emit does not need to synchronize its own state. Queued
// items are not started after cancellation; running measurements report their
// canceled/timeout status before this method returns.
func (s *Service) MeasureBatch(ctx context.Context, items []domain.Item, force bool, emit func(Result)) {
	items = append([]domain.Item(nil), items...)
	workers := min(s.concurrency, len(items))
	if workers == 0 {
		return
	}
	jobs := make(chan domain.Item)
	results := make(chan Result, workers)
	var group sync.WaitGroup
	for n := 0; n < workers; n++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for item := range jobs {
				results <- s.Measure(ctx, item, force)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, item := range items {
			if ctx.Err() != nil {
				return
			}
			select {
			case jobs <- item:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { group.Wait(); close(results) }()
	for result := range results {
		if emit != nil {
			emit(result)
		}
	}
}

func contextStatus(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "canceled"
}

// Because du runs in the selected directory against '.', no arbitrary filename
// appears in its output. Require exactly one numeric total and that sentinel.
func parseKiB(output []byte) (int64, error) {
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[1] != "." {
		return 0, errors.New("du did not return one directory total")
	}
	kib, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid du KiB total %q", domain.Display(fields[0]))
	}
	if kib > math.MaxInt64/1024 {
		return 0, errors.New("du total exceeds the supported byte count")
	}
	return int64(kib) * 1024, nil
}
