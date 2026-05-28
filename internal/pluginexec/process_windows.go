//go:build windows

package pluginexec

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"time"
)

type processRequest struct {
	Path    string
	Args    []string
	Dir     string
	Env     []string
	Timeout time.Duration
	Stdout  io.Writer
	Stderr  io.Writer
}

type exitError struct {
	Code int
}

func (e exitError) Error() string {
	return "process exited"
}

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

func processWaitError(err error) error {
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exitError{Code: exit.ExitCode()}
	}
	return err
}
