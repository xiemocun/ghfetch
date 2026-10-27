package source

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParsePrefixes(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"direct", []string{""}},
		{"direct,ghfast.top", []string{"", "https://ghfast.top/"}},
		{"https://mirror.example/proxy/", []string{"https://mirror.example/proxy/"}},
		{"mirror.example", []string{"https://mirror.example/"}},
	}
	for _, c := range cases {
		got, err := ParsePrefixes(c.in)
		if err != nil {
			t.Fatalf("ParsePrefixes(%q): %v", c.in, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("ParsePrefixes(%q) = %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ParsePrefixes(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestCandidatesPreservesOrder(t *testing.T) {
	c := New([]string{"", "https://a/", "https://b/"})
	got := c.Candidates("https://github.com/x/y/releases/download/v1/f.tgz")
	want := []string{
		"https://github.com/x/y/releases/download/v1/f.tgz",
		"https://a/https://github.com/x/y/releases/download/v1/f.tgz",
		"https://b/https://github.com/x/y/releases/download/v1/f.tgz",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d candidates", len(got))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("candidate %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGetFallsBackToSecondMirror(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer good.Close()

	c := New([]string{bad.URL + "/", good.URL + "/"})
	c.Retries = 0
	data, used, err := c.Get(context.Background(), "origin.bin")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(data) != "payload" {
		t.Errorf("data = %q", data)
	}
	if used != good.URL+"/origin.bin" {
		t.Errorf("used = %q", used)
	}
}

func TestGetReportsTotalFailure(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer bad.Close()

	c := New([]string{bad.URL + "/"})
	c.Retries = 0
	if _, _, err := c.Get(context.Background(), "origin.bin"); err == nil {
		t.Fatal("expected an error")
	}
}

// TestGetAbandonsSlowMirror is the behaviour the whole tool exists for: a
// source that answers but trickles must lose to the next one in the chain.
func TestGetAbandonsSlowMirror(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		_, _ = w.Write([]byte("tiny"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer slow.Close()

	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fast-payload"))
	}))
	defer fast.Close()

	c := New([]string{slow.URL + "/", fast.URL + "/"})
	c.Retries = 0
	c.MinBytes = 128 << 10
	c.Window = 100 * time.Millisecond
	c.Stall = 300 * time.Millisecond

	data, used, err := c.Get(context.Background(), "origin.bin")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(data) != "fast-payload" {
		t.Errorf("data = %q", data)
	}
	if !strings.HasPrefix(used, fast.URL) {
		t.Errorf("served by %q, want the fast mirror", used)
	}
}

func TestGetFailsWhenOnlySourceIsSlow(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tiny"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer slow.Close()

	c := New([]string{slow.URL + "/"})
	c.Retries = 0
	c.MinBytes = 128 << 10
	c.Window = 100 * time.Millisecond
	c.Stall = 300 * time.Millisecond

	_, _, err := c.Get(context.Background(), "origin.bin")
	if !errors.Is(err, ErrUnusable) {
		t.Fatalf("err = %v, want ErrUnusable", err)
	}
}
