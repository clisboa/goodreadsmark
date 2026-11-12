package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 8 * 1024 * 1024

func newHTTPClient(timeout time.Duration, transport http.RoundTripper) *http.Client {
	if transport == nil {
		transport = &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: timeout,
			IdleConnTimeout: 90 * time.Second, MaxIdleConns: 4,
			TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
		}
	}
	return &http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type requestOptions struct {
	Client               *http.Client
	Method, URL, Service string
	Headers              http.Header
	Body                 []byte
	Limit                int
}

func request(ctx context.Context, opts requestOptions) ([]byte, error) {
	write := opts.Method != http.MethodGet
	req, err := http.NewRequestWithContext(ctx, opts.Method, opts.URL, bytes.NewReader(opts.Body))
	if err != nil {
		return nil, &RemoteError{Message: opts.Service + " request is invalid", cause: err}
	}
	req.Header = opts.Headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("User-Agent", "goodreadsmark/"+Version)
	req.GetBody = nil // A write must never be automatically replayed by the transport.
	client := *opts.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return nil, &RemoteError{Message: opts.Service + " transport failed", Ambiguous: write, cause: err}
	}
	if res.StatusCode >= 300 {
		closeErr := res.Body.Close()
		remote := &RemoteError{Status: res.StatusCode, cause: closeErr}
		if res.StatusCode < 400 {
			remote.Message = opts.Service + " redirect refused"
			return nil, remote
		}
		remote.Message = fmt.Sprintf("%s returned HTTP %d", opts.Service, res.StatusCode)
		remote.Ambiguous = write && (res.StatusCode >= 500 || res.StatusCode == 408)
		if res.StatusCode == 429 || res.StatusCode == 503 {
			remote.RetryAfter = retryAfter(res.Header.Get("Retry-After"), time.Now())
		}
		return nil, remote
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = maxResponseBytes
	}
	data, readErr := io.ReadAll(io.LimitReader(res.Body, int64(limit)+1))
	closeErr := res.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, &RemoteError{Message: opts.Service + " response read failed", Ambiguous: write, cause: err}
	}
	if len(data) > limit {
		return nil, &RemoteError{Message: opts.Service + " response exceeds size limit", Ambiguous: write}
	}
	return data, nil
}

func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		maxSeconds := int64(time.Duration(1<<63-1) / time.Second)
		return time.Duration(min(max(seconds, 0), maxSeconds)) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(date.Sub(now), 0)
	}
	return time.Minute
}

func globalRemote(err error) bool {
	remote, ok := remoteError(err)
	return ok && (remote.Auth() || remote.Status == 429 || remote.RetryAfter > 0)
}
