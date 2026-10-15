// Package doctor checks that a worker machine is ready to serve jobs, and says
// exactly what is wrong when it is not.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/protocol"
)

// Result is one check's outcome.
type Result struct {
	Name   string
	OK     bool
	Detail string
}

// Ollama is the part of *ollama.Client the checks use.
type Ollama interface {
	Version(ctx context.Context) (string, error)
	List(ctx context.Context) ([]string, error)
	Generate(ctx context.Context, r ollama.GenerateRequest) (string, error)
}

// Doctor holds the configuration under test and every seam to the machine.
type Doctor struct {
	ServerURL  string
	Token      string
	Version    string
	Models     []string
	OllamaPort string // default 11434
	// Remote: Ollama runs on another host of the private network (the
	// -ollama-remote opt-in). This worker does not manage it, so the local
	// install and exposure checks do not apply.
	Remote bool

	Ollama         Ollama
	Detect         func(ctx context.Context) ollama.Detection
	HTTP           *http.Client
	Getenv         func(string) string
	InterfaceAddrs func() ([]net.Addr, error)
	Dial           func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Run performs every check in order.
func (d *Doctor) Run(ctx context.Context) []Result {
	rs := []Result{d.installed(ctx), d.reachable(ctx), d.notExposed(ctx)}
	rs = append(rs, d.models(ctx)...)
	return append(rs, d.server(ctx), d.credential(ctx), d.latency(ctx))
}

// Print writes rs as a readable list and reports whether every check passed.
func Print(w io.Writer, rs []Result) bool {
	ok := true
	for _, r := range rs {
		mark := "ok  "
		if !r.OK {
			mark, ok = "FAIL", false
		}
		fmt.Fprintf(w, "[%s] %s: %s\n", mark, r.Name, r.Detail)
	}
	return ok
}

func pass(name, detail string) Result { return Result{name, true, detail} }
func fail(name, detail string) Result { return Result{name, false, detail} }

func (d *Doctor) installed(ctx context.Context) Result {
	const name = "Ollama installed"
	if d.Remote {
		return pass(name, "remote Ollama on the private network; installed and managed on its own host")
	}
	det := d.Detect(ctx)
	switch {
	case det.Path != "":
		return pass(name, det.Path)
	case det.Installed:
		return pass(name, "binary not found, but the API answers")
	}
	return fail(name, "not found; run `dumb-worker setup` to install it")
}

func (d *Doctor) reachable(ctx context.Context) Result {
	const name = "Ollama reachable"
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	v, err := d.Ollama.Version(ctx)
	if err != nil {
		return fail(name, err.Error())
	}
	if d.Remote {
		return pass(name, "version "+v+" on a private-network host (remote mode)")
	}
	return pass(name, "version "+v+" on loopback")
}

func (d *Doctor) notExposed(ctx context.Context) Result {
	const name = "Ollama not exposed"
	if d.Remote {
		return pass(name, "not checked in remote mode: the remote Ollama listens on the private network by design; keep that network trusted")
	}
	if h := d.Getenv("OLLAMA_HOST"); h != "" && !loopbackHostSetting(h) {
		return fail(name, "OLLAMA_HOST="+h+" makes Ollama listen beyond loopback; unset it or use 127.0.0.1")
	}
	port := d.OllamaPort
	if port == "" {
		port = "11434"
	}
	addrs, err := d.InterfaceAddrs()
	if err != nil {
		return fail(name, "cannot list interface addresses: "+err.Error())
	}
	tried := 0
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		// Link-local addresses need a zone to dial; they are not routable anyway.
		if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
			continue
		}
		tried++
		addr := net.JoinHostPort(ipn.IP.String(), port)
		dctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		c, err := d.Dial(dctx, "tcp", addr)
		cancel()
		if err == nil {
			c.Close()
			return fail(name, "Ollama is reachable from the network at "+addr+"; bind it to 127.0.0.1")
		}
	}
	return pass(name, fmt.Sprintf("port %s closed on %d non-loopback address(es)", port, tried))
}

// loopbackHostSetting interprets OLLAMA_HOST ("host", "host:port", "scheme://host:port").
func loopbackHostSetting(v string) bool {
	if i := strings.Index(v, "://"); i >= 0 {
		v = v[i+3:]
	}
	v, _, _ = strings.Cut(v, "/")
	host := v
	if h, _, err := net.SplitHostPort(v); err == nil {
		host = h
	}
	return host != "" && ollama.IsLoopbackHost(host)
}

func (d *Doctor) models(ctx context.Context) []Result {
	if len(d.Models) == 0 {
		return []Result{fail("Models", "no model configured (DUMB_MODELS)")}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	names, err := d.Ollama.List(ctx)
	var rs []Result
	for _, m := range d.Models {
		name := "Model " + m
		switch {
		case err != nil:
			rs = append(rs, fail(name, "cannot list models: "+err.Error()))
		case ollama.HasModel(names, m):
			rs = append(rs, pass(name, "present"))
		case d.Remote:
			rs = append(rs, fail(name, "missing on the remote Ollama; run `ollama pull "+m+"` on that host (the worker never pulls in remote mode)"))
		default:
			rs = append(rs, fail(name, "missing; run `dumb-worker setup` or `ollama pull "+m+"`"))
		}
	}
	return rs
}

func (d *Doctor) server(ctx context.Context) Result {
	const name = "Server reachable"
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.ServerURL, nil)
	if err != nil {
		return fail(name, err.Error())
	}
	res, err := d.HTTP.Do(req)
	if err != nil {
		return fail(name, err.Error())
	}
	res.Body.Close()
	return pass(name, d.ServerURL+" answered HTTP "+fmt.Sprint(res.StatusCode))
}

func (d *Doctor) credential(ctx context.Context) Result {
	const name = "Credential and version"
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	u, err := url.Parse(d.ServerURL)
	if err != nil {
		return fail(name, err.Error())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.JoinPath("/v1/workers/me").String(), nil)
	if err != nil {
		return fail(name, err.Error())
	}
	req.Header.Set("Authorization", "Bearer "+d.Token)
	req.Header.Set(protocol.WorkerVersionHeader, d.Version)
	res, err := d.HTTP.Do(req)
	if err != nil {
		return fail(name, err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	switch res.StatusCode {
	case http.StatusOK:
		var me protocol.WhoAmI
		json.Unmarshal(b, &me)
		return pass(name, "accepted as worker "+me.WorkerID+" (version "+d.Version+", minimum "+me.MinWorkerVersion+")")
	case http.StatusUnauthorized:
		return fail(name, "credential rejected")
	case http.StatusUpgradeRequired:
		var ue protocol.UpgradeError
		json.Unmarshal(b, &ue)
		return fail(name, "worker too old, minimum "+ue.MinWorkerVersion+" (this is "+d.Version+")")
	}
	return fail(name, fmt.Sprintf("unexpected HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(b))))
}

func (d *Doctor) latency(ctx context.Context) Result {
	const name = "Sample latency"
	if len(d.Models) == 0 {
		return fail(name, "no model configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	start := time.Now()
	out, err := d.Ollama.Generate(ctx, ollama.GenerateRequest{Model: d.Models[0], Prompt: "Reply with the single word: ok"})
	ms := time.Since(start).Milliseconds()
	switch {
	case err != nil:
		return fail(name, err.Error())
	case strings.TrimSpace(out) == "":
		return fail(name, "empty response from "+d.Models[0])
	}
	return pass(name, fmt.Sprintf("%d ms on %s", ms, d.Models[0]))
}
