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
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		return processWaitError(err)
	case <-runCtx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
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
