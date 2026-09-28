package decide

import (
	"net/http"
	"time"
)

// HTTPClient exposes the client every request goes through.
func (c *Client) HTTPClient() *http.Client { return c.http }

// NewWithTimeouts builds a Client with shortened bounds, so a test can prove what
// happens at a timeout, and after a retry, without waiting a minute for either.
func NewWithTimeouts(cfg Config, decision, ready, backoff time.Duration) (*Client, error) {
	return newClient(cfg, timeouts{decision: decision, ready: ready, backoff: backoff})
}
