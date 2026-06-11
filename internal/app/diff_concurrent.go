package app

import (
	"context"
	"sync"
)

// splitSideParallelism divides one render-parallelism budget across the two
// diff sides. Below 2 there is nothing to split and sides run sequentially.
func splitSideParallelism(parallelism int) (left, right int, concurrent bool) {
	if parallelism < 2 {
		return parallelism, parallelism, false
	}
	left = (parallelism + 1) / 2
	return left, parallelism - left, true
}

type diffSideOutcome struct {
	result BuildResult
	err    error
}

func runDiffSidePair(ctx context.Context, concurrent bool, run func(context.Context, BuildRequest) (BuildResult, error), leftRequest, rightRequest BuildRequest) (diffSideOutcome, diffSideOutcome) {
	if !concurrent {
		var left, right diffSideOutcome
		left.result, left.err = run(ctx, leftRequest)
		right.result, right.err = run(ctx, rightRequest)
		return left, right
	}
	var left, right diffSideOutcome
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		left.result, left.err = run(ctx, leftRequest)
	}()
	go func() {
		defer wg.Done()
		right.result, right.err = run(ctx, rightRequest)
	}()
	wg.Wait()
	return left, right
}
