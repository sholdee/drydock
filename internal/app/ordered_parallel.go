package app

import (
	"context"
	"sync"
)

type orderedParallelOptions[T any] struct {
	total       int
	parallelism int
	run         func(context.Context, int) T
	onComplete  func(T, int, int) error
	// shouldStop stops the run at a result: jobs after its index are
	// cancelled or never run, while earlier jobs, whose results an ordered
	// caller still reads, run to completion. A stop does not cancel ctx.
	shouldStop func(T) bool
}

type indexedOrderedResult[T any] struct {
	index  int
	result T
}

// runOrderedParallel runs options.run for every index on up to
// options.parallelism workers, dispatching in index order, and returns the
// results by index. completed[i] is false for a job that never ran: one
// skipped after a stop, or never dispatched because ctx was cancelled.
//
// The error is the first options.onComplete error, else ctx.Err() when ctx
// was cancelled before a stop; after a stop the error is nil even if ctx was
// also cancelled, so a caller that stops must scan the results in index
// order and act on the first result that asked to stop.
func runOrderedParallel[T any](ctx context.Context, options orderedParallelOptions[T]) ([]T, []bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	workerCount := orderedParallelWorkerCount(options.total, options.parallelism)
	jobs := make(chan int)
	resultsCh := make(chan indexedOrderedResult[T], options.total)
	results := make([]T, options.total)
	completedResults := make([]bool, options.total)
	stop := newOrderedParallelStop(options.total)

	launchOrderedParallelWorkers(ctx, jobs, resultsCh, workerCount, stop, options.run)
	scheduleErrCh := scheduleOrderedParallelJobs(ctx, jobs, options.total, stop.stopped)

	callbackErr, stopRequested := collectOrderedParallelResults(resultsCh, results, completedResults, cancel, stop, options)
	scheduleErr := <-scheduleErrCh

	if callbackErr != nil {
		return results, completedResults, callbackErr
	}
	if scheduleErr != nil && !stopRequested {
		return results, completedResults, scheduleErr
	}
	return results, completedResults, nil
}

func orderedParallelWorkerCount(total, parallelism int) int {
	workerCount := min(parallelism, total)
	if workerCount < 1 && total > 0 {
		workerCount = 1
	}
	return workerCount
}

func launchOrderedParallelWorkers[T any](
	ctx context.Context,
	jobs <-chan int,
	resultsCh chan<- indexedOrderedResult[T],
	workerCount int,
	stop *orderedParallelStop,
	run func(context.Context, int) T,
) {
	var wg sync.WaitGroup
	for range workerCount {
		wg.Go(func() {
			for index := range jobs {
				jobCtx, ok := stop.start(ctx, index)
				if !ok {
					continue
				}
				var result T
				func() {
					// finish cancels the job context on every exit, including
					// a panic recovered further out.
					defer stop.finish(index)
					result = run(jobCtx, index)
				}()
				resultsCh <- indexedOrderedResult[T]{
					index:  index,
					result: result,
				}
			}
		})
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()
}

func scheduleOrderedParallelJobs(ctx context.Context, jobs chan<- int, total int, stopped <-chan struct{}) <-chan error {
	scheduleErrCh := make(chan error, 1)
	go func() {
		defer close(jobs)
		for index := range total {
			select {
			case <-ctx.Done():
				scheduleErrCh <- ctx.Err()
				return
			case <-stopped:
				scheduleErrCh <- nil
				return
			case jobs <- index:
			}
		}
		scheduleErrCh <- nil
	}()
	return scheduleErrCh
}

func collectOrderedParallelResults[T any](
	resultsCh <-chan indexedOrderedResult[T],
	results []T,
	completedResults []bool,
	cancel context.CancelFunc,
	stop *orderedParallelStop,
	options orderedParallelOptions[T],
) (error, bool) {
	var callbackErr error
	// stopRequested hides the scheduler's ctx error when a stop, which closes
	// the dispatch loop on its own, races a caller cancellation.
	stopRequested := false
	completed := 0
	for indexed := range resultsCh {
		results[indexed.index] = indexed.result
		completedResults[indexed.index] = true
		completed++
		if options.shouldStop != nil && options.shouldStop(indexed.result) {
			stopRequested = true
			stop.after(indexed.index)
		}
		if callbackErr != nil || options.onComplete == nil {
			continue
		}
		if err := options.onComplete(indexed.result, completed, options.total); err != nil {
			callbackErr = err
			stopRequested = true
			cancel()
		}
	}
	return callbackErr, stopRequested
}

// orderedParallelStop cuts a run off after the lowest index whose result
// asked to stop. Jobs are dispatched in index order, so every earlier job
// has already been dispatched and still runs to completion, while later jobs
// in flight are cancelled and later jobs not yet started are skipped.
type orderedParallelStop struct {
	mu        sync.Mutex
	index     int
	requested bool
	stopped   chan struct{}
	running   map[int]context.CancelFunc
}

func newOrderedParallelStop(total int) *orderedParallelStop {
	return &orderedParallelStop{
		index:   total,
		stopped: make(chan struct{}),
		running: map[int]context.CancelFunc{},
	}
}

// start returns the context job index runs under, or false when the job
// follows the stop and must not run.
func (s *orderedParallelStop) start(ctx context.Context, index int) (context.Context, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index > s.index {
		return nil, false
	}
	jobCtx, cancel := context.WithCancel(ctx)
	s.running[index] = cancel
	return jobCtx, true
}

func (s *orderedParallelStop) finish(index int) {
	s.mu.Lock()
	cancel := s.running[index]
	delete(s.running, index)
	s.mu.Unlock()
	cancel()
}

// after stops the run after index, cancelling the later jobs in flight.
func (s *orderedParallelStop) after(index int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index >= s.index {
		return
	}
	s.index = index
	if !s.requested {
		s.requested = true
		close(s.stopped)
	}
	for running, cancel := range s.running {
		if running > index {
			cancel()
		}
	}
}
