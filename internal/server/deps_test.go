package server

import (
	"os/exec"
	"strings"
	"testing"
)

// TestServerHasNoThirdPartyDeps keeps golang.org/x/crypto (used by the
// worker's self-updater) and every other non-stdlib module out of the server.
func TestServerHasNoThirdPartyDeps(t *testing.T) {
	for _, pkg := range []string{"github.com/gkhntpbs/dumb/cmd/dumb-server", "github.com/gkhntpbs/dumb/internal/server"} {
		out, err := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("go list %s: %v\n%s", pkg, err, out)
		}
		for _, dep := range strings.Fields(string(out)) {
			if !strings.HasPrefix(dep, "github.com/gkhntpbs/dumb/") {
				t.Errorf("%s depends on %s", pkg, dep)
			}
			if strings.HasPrefix(dep, "github.com/gkhntpbs/dumb/internal/update") {
				t.Errorf("%s depends on the worker's updater %s", pkg, dep)
			}
		}
	}
}
