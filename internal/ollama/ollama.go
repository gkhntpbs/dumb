// Package ollama talks to the local Ollama HTTP API and manages the Ollama install.
//
// The client only ever talks to a loopback address: a worker's models are never
// reached over the network, and a misconfigured URL fails loudly instead.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

// DefaultURL is where Ollama listens out of the box.
const DefaultURL = "http://127.0.0.1:11434"

// IsLoopbackHost reports whether host (no port) is localhost or a loopback IP.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

type Client struct {
	base *url.URL
	http *http.Client
}

// New returns a client for baseURL ("" means DefaultURL). It refuses any
// non-loopback host.
func New(baseURL string) (*Client, error) {
	if baseURL == "" {
		baseURL = DefaultURL
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("ollama: %q: scheme must be http or https", baseURL)
	}
	if !IsLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("ollama: %q is not a loopback address; Ollama must only be used on 127.0.0.1", baseURL)
	}
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("ollama: redirects are not followed")
	}}
	return &Client{base: u, http: hc}, nil
}

// Port returns the port the client talks to.
func (c *Client) Port() string {
	if p := c.base.Port(); p != "" {
		return p
	}
	if c.base.Scheme == "https" {
		return "443"
	}
	return "80"
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.JoinPath(path).String(), rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("ollama: %s %s: %d: %s", method, path, res.StatusCode, e.Error)
		}
		return nil, fmt.Errorf("ollama: %s %s: %d", method, path, res.StatusCode)
	}
	return res, nil
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	res, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return json.NewDecoder(res.Body).Decode(v)
}

// Version returns the running Ollama's version.
func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	if err := c.getJSON(ctx, "/api/version", &v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// List returns the names of the models present locally.
func (c *Client) List(ctx context.Context) ([]string, error) {
	var v struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := c.getJSON(ctx, "/api/tags", &v); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(v.Models))
	for _, m := range v.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

// HasModel reports whether name is in names, treating "x" as "x:latest".
func HasModel(names []string, name string) bool {
	for _, n := range names {
		if n == name || (!strings.Contains(name, ":") && n == name+":latest") {
			return true
		}
	}
	return false
}

// Progress is one status line of a pull.
type Progress struct {
	Status    string `json:"status"`
	Digest    string `json:"digest,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Completed int64  `json:"completed,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Pull downloads model, calling progress (if non-nil) for every status line.
// It succeeds only when Ollama reports "success".
func (c *Client) Pull(ctx context.Context, model string, progress func(Progress)) error {
	res, err := c.do(ctx, http.MethodPost, "/api/pull", map[string]any{"model": model, "stream": true})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var p Progress
		if err := json.Unmarshal(sc.Bytes(), &p); err != nil {
			return fmt.Errorf("ollama: pull %s: bad status line: %w", model, err)
		}
		if p.Error != "" {
			return fmt.Errorf("ollama: pull %s: %s", model, p.Error)
		}
		if progress != nil {
			progress(p)
		}
		if p.Status == "success" {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("ollama: pull %s: %w", model, err)
	}
	return fmt.Errorf("ollama: pull %s: stream ended without success", model)
}

// GenerateRequest is one non-streaming completion.
type GenerateRequest struct {
	Model      string
	Prompt     string
	System     string
	Format     string // "json" or ""
	NumPredict int
}

// Generate runs one completion and returns the response text.
func (c *Client) Generate(ctx context.Context, r GenerateRequest) (string, error) {
	body := map[string]any{"model": r.Model, "prompt": r.Prompt, "stream": false}
	if r.System != "" {
		body["system"] = r.System
	}
	if r.Format != "" {
		body["format"] = r.Format
	}
	if r.NumPredict == 0 {
		r.NumPredict = protocol.MaxNumPredict
	}
	if r.NumPredict < 1 || r.NumPredict > protocol.MaxNumPredict {
		return "", errors.New("ollama: num_predict out of range")
	}
	body["options"] = map[string]int{"num_predict": r.NumPredict}
	res, err := c.do(ctx, http.MethodPost, "/api/generate", body)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var v struct {
		Response string `json:"response"`
		Error    string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		return "", fmt.Errorf("ollama: generate: %w", err)
	}
	if v.Error != "" {
		return "", fmt.Errorf("ollama: generate: %s", v.Error)
	}
	return v.Response, nil
}
