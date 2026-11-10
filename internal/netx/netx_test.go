package netx

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A server that accepts the request and never answers, like a stalled API.
func hanging(t *testing.T) *httptest.Server {
	stop := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(stop); s.Close() })
	return s
}

// A stalled request must fail, not freeze the pane: a refresh blocks keys and redraws.
func TestHTTPClientGivesUp(t *testing.T) {
	s := hanging(t)
	start := time.Now()
	_, err := NewHTTPClient(200 * time.Millisecond).Get(s.URL)
	if err == nil {
		t.Fatal("request to a stalled server succeeded")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("gave up after %v", d)
	}
	if HTTP.Timeout <= 0 || HTTP.Timeout > time.Minute {
		t.Fatalf("shared client timeout %v", HTTP.Timeout)
	}
}
