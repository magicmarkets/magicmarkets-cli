package magicmarkets

import (
	"context"
	"fmt"
	"net/url"
)

// Heartbeat timeout bounds enforced by the API.
const (
	MinHeartbeatTimeout = 10
	MaxHeartbeatTimeout = 300
)

// CreateHeartbeat starts a dead-man's switch. If it is not refreshed before it
// expires, every open order is closed automatically.
//
// timeout is in seconds and must be between 10 and 300.
func (c *Client) CreateHeartbeat(ctx context.Context, timeout int) (*Heartbeat, error) {
	if timeout < MinHeartbeatTimeout || timeout > MaxHeartbeatTimeout {
		return nil, fmt.Errorf("heartbeat timeout must be %d–%d seconds, got %d",
			MinHeartbeatTimeout, MaxHeartbeatTimeout, timeout)
	}
	body := struct {
		Timeout int `json:"timeout"`
	}{Timeout: timeout}

	var out Heartbeat
	if err := c.post(ctx, "/heartbeats/", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListHeartbeats returns the account's active heartbeats.
//
// This endpoint wraps its data under a "heartbeats" key, unlike every other
// list endpoint, which returns a flat array.
func (c *Client) ListHeartbeats(ctx context.Context) ([]Heartbeat, error) {
	var out struct {
		Heartbeats []Heartbeat `json:"heartbeats"`
	}
	if err := c.get(ctx, "/heartbeats/", nil, &out); err != nil {
		return nil, err
	}
	return out.Heartbeats, nil
}

// GetHeartbeat retrieves a single heartbeat.
func (c *Client) GetHeartbeat(ctx context.Context, heartbeatID string) (*Heartbeat, error) {
	var out Heartbeat
	if err := c.get(ctx, "/heartbeats/"+url.PathEscape(heartbeatID)+"/", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelHeartbeat stops a heartbeat without closing any orders.
func (c *Client) CancelHeartbeat(ctx context.Context, heartbeatID string) error {
	return c.delete(ctx, "/heartbeats/"+url.PathEscape(heartbeatID)+"/", nil)
}

// RefreshHeartbeat extends a heartbeat's expiry. Call this on an interval
// shorter than the timeout to keep the switch from firing.
func (c *Client) RefreshHeartbeat(ctx context.Context, heartbeatID string) (*Heartbeat, error) {
	var out Heartbeat
	if err := c.post(ctx, "/heartbeats/"+url.PathEscape(heartbeatID)+"/refresh/", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
