// Package netx is the HTTP client for the GitHub and Expo panes: one shared client, with a
// timeout so a stalled request fails instead of freezing the pane (a refresh blocks keys
// and redraws until it returns).
package netx

import (
	"net/http"
	"time"
)

// Timeout bounds every API request, connection to last byte.
const Timeout = 30 * time.Second

// HTTP is the shared client; it reuses connections across refreshes.
var HTTP = NewHTTPClient(Timeout)

func NewHTTPClient(d time.Duration) *http.Client { return &http.Client{Timeout: d} }
