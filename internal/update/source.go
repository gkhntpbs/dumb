package update

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/protocol"
)

// Channels.
const (
	ChannelStable = "stable" // releases only
	ChannelBeta   = "beta"   // pre-releases too
)

// Defaults for the release source.
const (
	DefaultBaseURL = "https://api.github.com"
	DefaultOwner   = "gkhntpbs"
	DefaultRepo    = "dumb"
)

// Release is one published GitHub release with a parseable vX.Y.Z[-pre] tag.
type Release struct {
	Tag        string
	Version    protocol.ReleaseVersion
	Prerelease bool
	Assets     map[string]string // name → download URL
}

// Source lists releases through the public GitHub REST API.
type Source struct {
	BaseURL     string // default DefaultBaseURL; https, or http on loopback
	Owner, Repo string // default DefaultOwner / DefaultRepo
	HTTP        *http.Client
	UserAgent   string
}

func (s *Source) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return http.DefaultClient
}

// checkURL requires https unless the host is loopback.
func checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("update: %q is not an absolute URL", raw)
	}
	if u.Scheme == "https" || (u.Scheme == "http" && ollama.IsLoopbackHost(u.Hostname())) {
		return nil
	}
	return fmt.Errorf("update: %q must use https (http is allowed only on loopback)", raw)
}

func (s *Source) get(ctx context.Context, raw string, limit int64) ([]byte, error) {
	if err := checkURL(raw); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json, application/octet-stream")
	if s.UserAgent != "" {
		req.Header.Set("User-Agent", s.UserAgent)
	}
	res, err := s.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: GET %s: %s", raw, res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("update: GET %s: %w", raw, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("update: GET %s: larger than %d bytes", raw, limit)
	}
	return b, nil
}

// Releases returns the published releases, drafts and unparseable tags skipped.
func (s *Source) Releases(ctx context.Context) ([]Release, error) {
	base := cmp.Or(s.BaseURL, DefaultBaseURL)
	u := strings.TrimRight(base, "/") + "/repos/" + url.PathEscape(cmp.Or(s.Owner, DefaultOwner)) + "/" +
		url.PathEscape(cmp.Or(s.Repo, DefaultRepo)) + "/releases?per_page=50"
	b, err := s.get(ctx, u, 8<<20)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("update: releases: %w", err)
	}
	var out []Release
	for _, r := range raw {
		if r.Draft || !strings.HasPrefix(r.TagName, "v") {
			continue
		}
		v, err := protocol.ParseReleaseVersion(r.TagName)
		if err != nil {
			continue
		}
		rel := Release{Tag: r.TagName, Version: v, Prerelease: r.Prerelease || v.IsPrerelease(), Assets: map[string]string{}}
		for _, a := range r.Assets {
			rel.Assets[a.Name] = a.URL
		}
		out = append(out, rel)
	}
	return out, nil
}

// ErrNoUpdate means no release qualifies.
var ErrNoUpdate = errors.New("update: no newer release in this channel")

// Pick returns the newest release in channel that is newer than current, at
// least min (when min is non-nil) and not in skip.
func Pick(rels []Release, channel string, current protocol.ReleaseVersion, min *protocol.ReleaseVersion, skip []string) (Release, error) {
	var best *Release
	for i := range rels {
		r := &rels[i]
		switch {
		case (r.Prerelease || r.Version.IsPrerelease()) && channel != ChannelBeta,
			!current.Less(r.Version),
			min != nil && r.Version.Less(*min),
			slices.Contains(skip, r.Version.String()):
			continue
		}
		if best == nil || best.Version.Less(r.Version) {
			best = r
		}
	}
	if best == nil {
		return Release{}, ErrNoUpdate
	}
	return *best, nil
}
