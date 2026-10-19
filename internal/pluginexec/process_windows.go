//go:build windows

package pluginexec

import (
	"context"
	"errors"
	"os/exec"
)

func runProcess(ctx context.Context, request processRequest) error {
	runCtx := ctx
	cancel := func() {}
	if request.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, request.Timeout)
	}
	defer cancel()

	cmd := exec.Command(request.Path)
	cmd.Args = append([]string(nil), request.Args...)
	cmd.Dir = request.Dir
	cmd.Env = append([]string(nil), request.Env...)
	cmd.Stdin = request.Stdin
	cmd.Stdout = request.Stdout
	cmd.Stderr = request.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	var waitErr error
	done := make(chan struct{})
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
		return processWaitError(waitErr)
	case <-runCtx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		// Reap the killed process. Its wait error is the kill signal; the
		// context error below is what the caller needs.
		<-done
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return runCtx.Err()
	}
}
