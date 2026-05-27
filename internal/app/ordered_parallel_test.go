package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestRunOrderedParallelReturnsResultsInInputOrder(t *testing.T) {
	started := make(chan int, 3)
	release := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}

	resultCh := make(chan struct {
		results   []int
		completed []bool
		err       error
	}, 1)
	go func() {
		results, completed, err := runOrderedParallel(context.Background(), orderedParallelOptions[int]{
			total:       3,
			parallelism: 3,
			run: func(_ context.Context, index int) int {
				started <- index
				<-release[index]
				return index * 10
			},
		})
		resultCh <- struct {
			results   []int
			completed []bool
			err       error
		}{results: results, completed: completed, err: err}
	}()

	waitStartedIndexes(t, started, 0, 1, 2)
	close(release[2])
	close(release[1])
	close(release[0])

	out := <-resultCh
	if out.err != nil {
		t.Fatalf("runOrderedParallel() error = %v", out.err)
	}
	if !reflect.DeepEqual(out.results, []int{0, 10, 20}) {
		t.Fatalf("results = %#v, want input order", out.results)
	}
	if !reflect.DeepEqual(out.completed, []bool{true, true, true}) {
		t.Fatalf("completed = %#v, want all completed", out.completed)
	}
}

func TestRunOrderedParallelCompletionHookReportsCompletionOrderCounts(t *testing.T) {
	started := make(chan int, 3)
	release := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	events := make(chan string, 3)

	resultCh := make(chan error, 1)
	go func() {
		_, _, err := runOrderedParallel(context.Background(), orderedParallelOptions[int]{
			total:       3,
			parallelism: 3,
			run: func(_ context.Context, index int) int {
				started <- index
				<-release[index]
				return index
			},
			onComplete: func(result, completed, total int) error {
				events <- eventSummary(result, completed, total)
				return nil
			},
		})
		resultCh <- err
	}()

	waitStartedIndexes(t, started, 0, 1, 2)
	close(release[2])
	assertNextEvent(t, events, "2:1/3")
	close(release[0])
	assertNextEvent(t, events, "0:2/3")
	close(release[1])
	assertNextEvent(t, events, "1:3/3")

	if err := <-resultCh; err != nil {
		t.Fatalf("runOrderedParallel() error = %v", err)
	}
}

func TestRunOrderedParallelCallbackErrorCancelsAndWins(t *testing.T) {
	callbackErr := errors.New("callback failed")

	_, completed, err := runOrderedParallel(context.Background(), orderedParallelOptions[int]{
		total:       10,
		parallelism: 1,
		run: func(ctx context.Context, index int) int {
			if index == 0 {
				return index
			}
			<-ctx.Done()
			return index
		},
		onComplete: func(int, int, int) error {
			return callbackErr
		},
	})

	if !errors.Is(err, callbackErr) {
		t.Fatalf("runOrderedParallel() error = %v, want callback error", err)
	}
	if !completed[0] {
		t.Fatalf("completed = %#v, want first result retained", completed)
	}
}

func TestRunOrderedParallelResultCancelRetainsCompletedPartials(t *testing.T) {
	type result struct {
		value int
		err   error
	}

	resultErr := errors.New("job failed")
	started := make(chan int, 2)
	release := []chan struct{}{make(chan struct{}), make(chan struct{})}

	resultCh := make(chan struct {
		results   []result
		completed []bool
		err       error
	}, 1)
	go func() {
		results, completed, err := runOrderedParallel(context.Background(), orderedParallelOptions[result]{
			total:       3,
			parallelism: 2,
			run: func(_ context.Context, index int) result {
				if index < len(release) {
					started <- index
					<-release[index]
				}
				if index == 0 {
					return result{value: index, err: resultErr}
				}
				return result{value: index}
			},
			shouldCancel: func(result result) bool {
				return result.err != nil
			},
		})
		resultCh <- struct {
			results   []result
			completed []bool
			err       error
		}{results: results, completed: completed, err: err}
	}()

	waitStartedIndexes(t, started, 0, 1)
	close(release[1])
	close(release[0])

	out := <-resultCh
	if out.err != nil {
		t.Fatalf("runOrderedParallel() error = %v", out.err)
	}
	if !out.completed[0] || !out.completed[1] {
		t.Fatalf("completed = %#v, want failed and successful partials retained", out.completed)
	}
	if !errors.Is(out.results[0].err, resultErr) || out.results[1].value != 1 {
		t.Fatalf("results = %#v, want ordered partial results", out.results)
	}
}

func eventSummary(result, completed, total int) string {
	return fmt.Sprintf("%d:%d/%d", result, completed, total)
}

func waitStartedIndexes(t *testing.T, started <-chan int, want ...int) {
	t.Helper()
	seen := make(map[int]bool, len(want))
	deadline := time.After(time.Second)
	for len(seen) < len(want) {
		select {
		case index := <-started:
			seen[index] = true
		case <-deadline:
			t.Fatalf("timed out waiting for started indexes, saw %#v want %#v", seen, want)
		}
	}
	for _, index := range want {
		if !seen[index] {
			t.Fatalf("started indexes = %#v, missing %d", seen, index)
		}
	}
}

func assertNextEvent(t *testing.T, events <-chan string, want string) {
	t.Helper()
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("event = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for event %q", want)
	}
}
