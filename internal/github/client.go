// Package github provides repository-scoped requests using GitHub CLI authentication.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/auth"
)

type Client struct {
	HTTP    *http.Client
	Base    string
	Repo    string
	stopped error // After a rate limit, stop issuing requests for this invocation.
}

func New(repo string) (*Client, error) {
	host, _ := auth.DefaultHost()
	if host != "github.com" {
		return nil, fmt.Errorf("this release supports github.com; active host is %s", host)
	}
	h, err := api.NewHTTPClient(api.ClientOptions{Host: host, Timeout: 30 * time.Second, Headers: map[string]string{"X-GitHub-Api-Version": "2022-11-28"}})
	if err != nil {
		return nil, err
	}
	return &Client{HTTP: h, Base: "https://api.github.com/repos/" + repo, Repo: repo}, nil
}

type Error struct {
	Status                int
	Method, Path, Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Message)
}
func Status(err error, code int) bool { var e *Error; return errors.As(err, &e) && e.Status == code }
func (c *Client) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	if c.stopped != nil {
		return nil, c.stopped
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		var msg struct{ Message string }
		_ = json.Unmarshal(raw, &msg)
		limited := resp.StatusCode == 429 || resp.StatusCode == 403 && (resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != "" || strings.Contains(strings.ToLower(msg.Message), "rate limit"))
		if limited {
			msg.Message += "; retry after GitHub's rate limit resets"
		}
		e := &Error{resp.StatusCode, method, path, msg.Message}
		if limited {
			c.stopped = e
		}
		return nil, e
	}
	return raw, nil
}
func (c *Client) Write(ctx context.Context, method, path string, body, out any) error {
	raw, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Snapshot caches observations for one planning pass only.
type Snapshot struct {
	ctx   context.Context
	c     *Client
	cache map[string]response
}
type response struct {
	raw []byte
	err error
}

func (c *Client) Snapshot(ctx context.Context) *Snapshot {
	return &Snapshot{ctx: ctx, c: c, cache: map[string]response{}}
}
func (s *Snapshot) Get(path string, out any) error {
	r, ok := s.cache[path]
	if !ok {
		r.raw, r.err = s.c.request(s.ctx, "GET", path, nil)
		s.cache[path] = r
	}
	if r.err != nil {
		return r.err
	}
	if out != nil {
		return json.Unmarshal(r.raw, out)
	}
	return nil
}
