package adminui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gkhntpbs/dumb/internal/queue"
)

// The panel states how long finished jobs stay listed; keep it equal to queue.Retention.
func TestRetentionNoteMatchesQueue(t *testing.T) {
	page, err := Files.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`Finished jobs stay listed for <span id="retention-seconds">%d</span> seconds;`, int(queue.Retention.Seconds()))
	if !strings.Contains(string(page), want) {
		t.Fatalf("index.html retention note does not match queue.Retention (%s); want %q", queue.Retention, want)
	}
}
