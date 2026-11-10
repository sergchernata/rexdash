package awsx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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

// AWS calls must give up too.
func TestAWSConfigGivesUp(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "x")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "y")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_PROFILE", "")
	s := hanging(t)
	cfg, err := awsConfig(context.Background(), 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", s.URL, nil)
	start := time.Now()
	if _, err := cfg.HTTPClient.Do(req); err == nil {
		t.Fatal("AWS client request to a stalled server succeeded")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("AWS client gave up after %v", d)
	}
}
