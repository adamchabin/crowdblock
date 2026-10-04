package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Client reports attacking addresses to the collector server
// (POST /api/v1/reports).
type Client struct {
	server string // base URL, e.g. https://crowdblock.example.org
	apiKey string
	http   *http.Client
}

func NewClient(server, apiKey string) *Client {
	return &Client{
		server: strings.TrimRight(server, "/"),
		apiKey: apiKey,
		http:   &http.Client{Timeout: 10 * time.Second},
	}
}

// ValidateServerURL rejects anything but an absolute http(s) URL, so that
// a typo shows up at start instead of at the first report.
func ValidateServerURL(server string) error {
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid server URL %q: expecting http://host[:port] or https://host[:port]", server)
	}
	return nil
}

// Report sends one attacking address; source is what detected it (the
// plugin name, e.g. "auth"), shown to the users of the list.
func (c *Client) Report(ctx context.Context, ip netip.Addr, source string) error {
	body, err := json.Marshal(map[string]string{"ip": ip.String(), "source": source})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.server+"/api/v1/reports", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}
