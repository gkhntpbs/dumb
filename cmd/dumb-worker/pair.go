package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/worker"
)

func exchangePair(ctx context.Context, client *http.Client, server, code string) (string, error) {
	u, err := worker.CheckServerURL(server)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("pair requires a valid HTTPS server URL")
	}
	raw, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil || len(raw) != 32 {
		return "", errors.New("invalid pairing code")
	}
	body, _ := json.Marshal(protocol.PairRequest{Code: code})
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.JoinPath("/v1/workers/pair").String(), bytes.NewReader(body))
	if err != nil {
		return "", errors.New("pair request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(protocol.WorkerVersionHeader, version)
	safe := *client
	safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := safe.Do(req)
	if err != nil {
		return "", errors.New("pair connection failed; obtain a new code if the exchange may have completed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("pair failed (HTTP %d); no automatic retry", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(data) > 65536 {
		return "", errors.New("invalid pairing response; obtain a new code")
	}
	var response protocol.IssuedCredential
	if json.Unmarshal(data, &response) != nil || response.Credential.Role != "worker" || response.Credential.CredentialID == "" || response.Credential.PrincipalID == "" || response.Credential.Revoked || !response.Credential.ExpiresAt.After(time.Now()) {
		return "", errors.New("invalid pairing response; obtain a new code")
	}
	raw, err = base64.RawURLEncoding.DecodeString(response.Token)
	if err != nil || len(raw) != 32 {
		return "", errors.New("invalid pairing credential; obtain a new code")
	}
	return response.Token, nil
}

func pairTarget(path string) error {
	if path == "" {
		return errors.New("pair requires -token-file")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return errors.New("invalid token path")
	}
	// Reject symlink ancestors as well as a symlink token target.
	for p := absolute; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil && !(p == absolute && os.IsNotExist(e)) {
			return errors.New("token parent directory must exist")
		}
		if e == nil && (info.Mode()&os.ModeSymlink != 0 || (p == absolute && !info.Mode().IsRegular())) {
			return errors.New("token path must not contain symlinks or special files")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if _, e := os.Lstat(path); e == nil {
		if _, e = worker.ReadTokenFile(path); e != nil {
			return errors.New("existing token file is not restricted")
		}
	}
	return pairParent(filepath.Dir(absolute))
}
func storePairToken(path, token string) error {
	if err := pairTarget(path); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".dumb-pair-*")
	if err != nil {
		return errors.New("cannot stage token file")
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if err = pairRestrict(name); err != nil {
		return errors.New("cannot restrict token file")
	}
	if _, err = f.WriteString(token + "\n"); err != nil {
		return errors.New("cannot write token file")
	}
	if err = f.Sync(); err != nil {
		return errors.New("cannot sync token file")
	}
	if err = f.Close(); err != nil {
		return errors.New("cannot close token file")
	}
	if err = pairTarget(path); err != nil {
		return err
	}
	if err = pairReplace(name, path); err != nil {
		return errors.New("cannot replace token file")
	}
	return pairSyncDir(filepath.Dir(path))
}
func cmdPair(ctx context.Context, c *config, stdout, stderr io.Writer) int {
	if e := pairTarget(c.tokenFile); e != nil {
		fmt.Fprintln(stderr, e)
		return exitUsage
	}
	u, e := worker.CheckServerURL(c.server)
	if e != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		fmt.Fprintln(stderr, "pair requires a valid HTTPS server URL")
		return exitUsage
	}
	fmt.Fprint(stderr, "Pairing code (hidden): ")
	restore, e := pairHideInput(os.Stdin)
	if e != nil {
		fmt.Fprintln(stderr, "cannot disable input echo")
		return exitUsage
	}
	input := bufio.NewReader(io.LimitReader(os.Stdin, 128))
	code, e := input.ReadString('\n')
	restore()
	fmt.Fprintln(stderr)
	if e != nil && !errors.Is(e, io.EOF) {
		fmt.Fprintln(stderr, "cannot read pairing code")
		return exitUsage
	}
	code = strings.TrimSpace(code)
	token, e := exchangePair(ctx, http.DefaultClient, c.server, code)
	if e != nil {
		fmt.Fprintln(stderr, e)
		return exitError
	}
	if e = storePairToken(c.tokenFile, token); e != nil {
		fmt.Fprintln(stderr, "credential was issued but could not be stored; obtain a new pairing code:", e)
		return exitError
	}
	fmt.Fprintln(stdout, "Worker credential saved. Restart this worker through its own tray/menu or service to use it.")
	return exitOK
}

func parsePairFlags(args []string) (*config, error) {
	c := &config{}
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&c.server, "server", "", "HTTPS server URL")
	fs.StringVar(&c.tokenFile, "token-file", "", "restricted credential file")
	if fs.Parse(args) != nil || len(fs.Args()) != 0 {
		return nil, errors.New("usage: dumb-worker pair -server HTTPS_URL -token-file PATH; code is read only from hidden stdin")
	}
	return c, nil
}
