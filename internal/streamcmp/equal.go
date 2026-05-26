package streamcmp

import (
	"bytes"
	"errors"
	"io"
)

func Equal(left, right io.Reader) (bool, error) {
	leftBuf := make([]byte, 32*1024)
	rightBuf := make([]byte, 32*1024)
	for {
		leftN, leftErr := io.ReadFull(left, leftBuf)
		rightN, rightErr := io.ReadFull(right, rightBuf)
		if err := readFullError(leftErr); err != nil {
			return false, err
		}
		if err := readFullError(rightErr); err != nil {
			return false, err
		}
		if leftN != rightN || !bytes.Equal(leftBuf[:leftN], rightBuf[:rightN]) {
			return false, nil
		}
		leftDone := leftErr == io.EOF || leftErr == io.ErrUnexpectedEOF
		rightDone := rightErr == io.EOF || rightErr == io.ErrUnexpectedEOF
		if leftDone || rightDone {
			return leftDone && rightDone, nil
		}
	}
}

func readFullError(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil
	}
	return err
}
