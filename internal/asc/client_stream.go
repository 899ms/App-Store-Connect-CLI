package asc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// doStreamingRequest sends a request whose response body is consumed by the
// caller. Client.Timeout also covers that body copy, which aborts a large
// download mid-transfer, so the configured timeout is applied to every phase
// before the response headers (dial, TLS handshake, request write and server
// think time) while the body copy is bounded by the request context.
func doStreamingRequest(client *http.Client, req *http.Request) (*http.Response, error) {
	if client == nil {
		client = newDefaultHTTPClient(ResolveTimeout())
	}
	timeout := client.Timeout
	streaming := *client
	streaming.Timeout = 0
	if timeout <= 0 {
		return streaming.Do(req)
	}

	ctx, cancel := context.WithCancel(req.Context())
	var expired atomic.Bool
	timer := time.AfterFunc(timeout, func() {
		expired.Store(true)
		cancel()
	})

	resp, err := streaming.Do(req.WithContext(ctx))
	timer.Stop()
	if err != nil {
		cancel()
		if expired.Load() && req.Context().Err() == nil {
			return nil, fmt.Errorf("timed out after %s awaiting response headers: %w", timeout, err)
		}
		return nil, err
	}

	// The derived context has to outlive this call so the body stays readable;
	// closing the body releases it.
	resp.Body = &streamingResponseBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type streamingResponseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *streamingResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}
