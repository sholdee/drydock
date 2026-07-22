package pluginexec

import (
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
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
}

type exitError struct {
	Code int
}

func (e exitError) Error() string {
	return "process exited"
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
