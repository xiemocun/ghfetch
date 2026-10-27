// Package source turns an official GitHub download URL into an ordered chain of
// candidate URLs, so a throttled github.com can fall back to a mirror.
package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// MaxAssetSize caps a download. Release binaries are never this large; the cap
// exists so a misbehaving mirror cannot fill memory.
const MaxAssetSize = 512 << 20

// DefaultPrefixes is tried in order. The empty prefix means "talk to
// github.com directly", which is fastest where the network allows it.
var DefaultPrefixes = []string{"", "https://ghfast.top/", "https://gh-proxy.com/"}

const (
	defaultMinBytes = 256 << 10
	defaultWindow   = 8 * time.Second
	defaultStall    = 12 * time.Second
)

// ErrUnusable marks a source that answers but cannot deliver at a usable rate.
// Such a failure is not transient, so it is never retried: the chain moves to
// the next prefix immediately.
var ErrUnusable = errors.New("source unusable")

// Chain downloads through an ordered list of URL prefixes.
type Chain struct {
	Prefixes []string
	Client   *http.Client
	Retries  int
	// MinBytes must arrive inside Window or the mirror is abandoned and the
	// next candidate is tried. This is what makes the chain useful: a mirror
	// that answers at 12 KB/s is worse than the next one in the list.
	MinBytes int
	Window   time.Duration
	// Stall is how long a source may deliver nothing at all before it is
	// abandoned. Connections in mainland China often open and then sit silent,
	// which no overall timeout catches soon enough.
	Stall time.Duration
}

// New builds a Chain. An empty prefixes slice selects DefaultPrefixes.
func New(prefixes []string) *Chain {
	if len(prefixes) == 0 {
		prefixes = DefaultPrefixes
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		MaxIdleConns:          8,
	}
	return &Chain{
		Prefixes: prefixes,
		Client:   &http.Client{Timeout: 10 * time.Minute, Transport: transport},
		Retries:  1,
		MinBytes: defaultMinBytes,
		Window:   defaultWindow,
		Stall:    defaultStall,
	}
}

// ParsePrefixes reads the --source flag: comma separated, "direct" for the
// origin, and bare hosts get an https:// scheme plus a trailing slash.
func ParsePrefixes(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		switch strings.ToLower(part) {
		case "direct", "origin", "github":
			out = append(out, "")
			continue
		}
		if !strings.Contains(part, "://") {
			part = "https://" + part
		}
		if !strings.HasSuffix(part, "/") {
			part += "/"
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil, errors.New("--source listed no usable prefixes")
	}
	return out, nil
}

// Candidates expands one origin URL into one URL per prefix.
func (c *Chain) Candidates(origin string) []string {
	urls := make([]string, 0, len(c.Prefixes))
	for _, p := range c.Prefixes {
		urls = append(urls, p+origin)
	}
	return urls
}

// Get downloads origin, trying each prefix until one answers. It returns the
// bytes and the URL that actually served them.
func (c *Chain) Get(ctx context.Context, origin string) ([]byte, string, error) {
	var lastErr error
	for _, u := range c.Candidates(origin) {
		for attempt := 0; attempt <= c.Retries; attempt++ {
			if err := ctx.Err(); err != nil {
				return nil, u, err
			}
			data, err := c.fetch(ctx, u)
			if err == nil {
				return data, u, nil
			}
			lastErr = err
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, u, err
			}
			if errors.Is(err, ErrUnusable) {
				break // not transient: go straight to the next prefix
			}
		}
	}
	return nil, "", fmt.Errorf("every source failed for %s: %w", origin, lastErr)
}

type fetchResult struct {
	data []byte
	err  error
}

func (c *Chain) fetch(ctx context.Context, u string) ([]byte, error) {
	stall := c.Stall
	if stall <= 0 {
		stall = defaultStall
	}
	attemptCtx, cancelAttempt := context.WithCancel(ctx)
	defer cancelAttempt()

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ghfetch")

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	if resp.ContentLength > MaxAssetSize {
		return nil, fmt.Errorf("%s: %d bytes exceeds the %d byte limit", u, resp.ContentLength, MaxAssetSize)
	}

	window := c.Window
	if window <= 0 {
		window = defaultWindow
	}
	minBytes := c.MinBytes
	if minBytes <= 0 {
		minBytes = defaultMinBytes
	}

	// The body is read in a goroutine so the caller can enforce both a stall
	// deadline and a minimum throughput without waiting on a blocking Read.
	done := make(chan fetchResult, 1)
	progress := make(chan struct{}, 1)
	var received atomic.Int64
	go func() {
		var buf bytes.Buffer
		if resp.ContentLength > 0 {
			buf.Grow(int(resp.ContentLength))
		}
		start := time.Now()
		chunk := make([]byte, 64<<10)
		for {
			n, readErr := resp.Body.Read(chunk)
			if n > 0 {
				buf.Write(chunk[:n])
				received.Store(int64(buf.Len()))
				select {
				case progress <- struct{}{}:
				default:
				}
			}
			if buf.Len() > MaxAssetSize {
				done <- fetchResult{err: fmt.Errorf("%s: response exceeds the %d byte limit", u, MaxAssetSize)}
				return
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					if buf.Len() == 0 {
						done <- fetchResult{err: fmt.Errorf("%s: empty response", u)}
						return
					}
					done <- fetchResult{data: buf.Bytes()}
					return
				}
				done <- fetchResult{err: fmt.Errorf("%s: %w", u, readErr)}
				return
			}
			// Enforce a minimum average rate for the whole transfer, not a
			// one-off checkpoint: a source that bursts 300 KB and then crawls
			// must still lose.
			if elapsed := time.Since(start); elapsed > window {
				rate := float64(buf.Len()) / elapsed.Seconds()
				if rate < float64(minBytes)/window.Seconds() {
					done <- fetchResult{err: fmt.Errorf("%w: %s delivered %.0f KiB/s after %s",
						ErrUnusable, u, rate/1024, elapsed.Round(time.Second))}
					return
				}
			}
		}
	}()

	timer := time.NewTimer(stall)
	defer timer.Stop()
	for {
		select {
		case <-progress:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(stall)
		case res := <-done:
			return res.data, res.err
		case <-timer.C:
			cancelAttempt()
			if n := received.Load(); n > 0 {
				return nil, fmt.Errorf("%w: %s delivered %s before stalling for %s",
					ErrUnusable, u, humanBytes(int(n)), stall)
			}
			return nil, fmt.Errorf("%w: %s delivered nothing for %s", ErrUnusable, u, stall)
		case <-ctx.Done():
			cancelAttempt()
			return nil, ctx.Err()
		}
	}
}

func humanBytes(n int) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KiB", float64(n)/unit)
}