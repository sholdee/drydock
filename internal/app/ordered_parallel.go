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

func runOrderedParallel[T any](ctx context.Context, options orderedParallelOptions[T]) ([]T, []bool, error) {
	workerCount := options.parallelism
	if workerCount > options.total {
		workerCount = options.total
	}
	if workerCount < 1 && options.total > 0 {
		workerCount = 1
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type indexedResult struct {
		index  int
		result T
	}

	jobs := make(chan int)
	resultsCh := make(chan indexedResult, options.total)
	results := make([]T, options.total)
	completedResults := make([]bool, options.total)

	var wg sync.WaitGroup
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				resultsCh <- indexedResult{
					index:  index,
					result: options.run(ctx, index),
				}
			}
		}()
	}

	scheduleErrCh := make(chan error, 1)
	go func() {
		defer close(jobs)
		for index := range options.total {
			select {
			case <-ctx.Done():
				scheduleErrCh <- ctx.Err()
				return
			case jobs <- index:
			}
		}
		scheduleErrCh <- nil
	}()

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

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
	scheduleErr := <-scheduleErrCh

	if callbackErr != nil {
		return results, completedResults, callbackErr
	}
	if scheduleErr != nil && !cancelRequested {
		return results, completedResults, scheduleErr
	}
	return results, completedResults, nil
}
