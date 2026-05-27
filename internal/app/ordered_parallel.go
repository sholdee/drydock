package app

import (
	"context"
	"sync"
)

type orderedParallelOptions[T any] struct {
	total        int
	parallelism  int
	run          func(context.Context, int) T
	onComplete   func(T, int, int) error
	shouldCancel func(T) bool
}

type indexedOrderedResult[T any] struct {
	index  int
	result T
}

func runOrderedParallel[T any](ctx context.Context, options orderedParallelOptions[T]) ([]T, []bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	workerCount := orderedParallelWorkerCount(options.total, options.parallelism)
	jobs := make(chan int)
	resultsCh := make(chan indexedOrderedResult[T], options.total)
	results := make([]T, options.total)
	completedResults := make([]bool, options.total)

	launchOrderedParallelWorkers(ctx, jobs, resultsCh, workerCount, options.run)
	scheduleErrCh := scheduleOrderedParallelJobs(ctx, jobs, options.total)

	callbackErr, cancelRequested := collectOrderedParallelResults(resultsCh, results, completedResults, cancel, options)
	scheduleErr := <-scheduleErrCh

	if callbackErr != nil {
		return results, completedResults, callbackErr
	}
	if scheduleErr != nil && !cancelRequested {
		return results, completedResults, scheduleErr
	}
	return results, completedResults, nil
}

func orderedParallelWorkerCount(total, parallelism int) int {
	workerCount := parallelism
	if workerCount > total {
		workerCount = total
	}
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
	run func(context.Context, int) T,
) {
	var wg sync.WaitGroup
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				resultsCh <- indexedOrderedResult[T]{
					index:  index,
					result: run(ctx, index),
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()
}

func scheduleOrderedParallelJobs(ctx context.Context, jobs chan<- int, total int) <-chan error {
	scheduleErrCh := make(chan error, 1)
	go func() {
		defer close(jobs)
		for index := range total {
			select {
			case <-ctx.Done():
				scheduleErrCh <- ctx.Err()
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
	options orderedParallelOptions[T],
) (error, bool) {
	var callbackErr error
	cancelRequested := false
	completed := 0
	for indexed := range resultsCh {
		results[indexed.index] = indexed.result
		completedResults[indexed.index] = true
		completed++
		if options.shouldCancel != nil && options.shouldCancel(indexed.result) {
			cancelRequested = true
			cancel()
		}
		if callbackErr != nil || options.onComplete == nil {
			continue
		}
		if err := options.onComplete(indexed.result, completed, options.total); err != nil {
			callbackErr = err
			cancelRequested = true
			cancel()
		}
	}
	return callbackErr, cancelRequested
}
