package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

func newRequest(ctx context.Context, method, url string, body []byte) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	return http.NewRequestWithContext(ctx, method, url, reader)
}

// readAll caps what a source can hand us, so one chatty endpoint can't put the
// server into swap.
func readAll(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, 8<<20))
}
