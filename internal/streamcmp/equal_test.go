package streamcmp

import (
	"io"
	"testing"
)

func TestEqualHandlesUnevenReaderChunks(t *testing.T) {
	left := &shortReader{data: []byte("same-content"), chunk: 1}
	right := &shortReader{data: []byte("same-content"), chunk: 5}

	equal, err := Equal(left, right)
	if err != nil {
		t.Fatalf("Equal() error = %v", err)
	}
	if !equal {
		t.Fatal("Equal() = false, want true")
	}
}

func TestEqualDetectsSameSizeDifference(t *testing.T) {
	left := &shortReader{data: []byte("same-size-a"), chunk: 3}
	right := &shortReader{data: []byte("same-size-b"), chunk: 4}

	equal, err := Equal(left, right)
	if err != nil {
		t.Fatalf("Equal() error = %v", err)
	}
	if equal {
		t.Fatal("Equal() = true, want false")
	}
}

type shortReader struct {
	data  []byte
	chunk int
}

func (r *shortReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	if len(p) > r.chunk {
		p = p[:r.chunk]
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}
