package app

import (
	"context"
	"reflect"
	"testing"
)

func TestSplitSideParallelism(t *testing.T) {
	tests := []struct {
		in, left, right int
		concurrent      bool
	}{
		{0, 0, 0, false},
		{1, 1, 1, false},
		{2, 1, 1, true},
		{3, 2, 1, true},
		{8, 4, 4, true},
	}
	for _, test := range tests {
		left, right, concurrent := splitSideParallelism(test.in)
		if left != test.left || right != test.right || concurrent != test.concurrent {
			t.Fatalf("splitSideParallelism(%d) = (%d, %d, %t), want (%d, %d, %t)",
				test.in, left, right, concurrent, test.left, test.right, test.concurrent)
		}
	}
}

func TestDiffAppsParallelSideBuildsMatchSequential(t *testing.T) {
	root := t.TempDir()
	left := root + "/left"
	right := root + "/right"
	writeSimpleApp(t, left, "old")
	writeSimpleApp(t, right, "new")

	sequential, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:         left,
		RightPath:        right,
		ExecutionOptions: ExecutionOptions{Parallelism: 1},
		Unified:          3,
	})
	if err != nil {
		t.Fatalf("sequential DiffApps() error = %v", err)
	}
	parallel, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:         left,
		RightPath:        right,
		ExecutionOptions: ExecutionOptions{Parallelism: 8},
		Unified:          3,
	})
	if err != nil {
		t.Fatalf("parallel DiffApps() error = %v", err)
	}
	if !reflect.DeepEqual(parallel.Results, sequential.Results) {
		t.Fatalf("parallel Results = %#v, want %#v", parallel.Results, sequential.Results)
	}
}
