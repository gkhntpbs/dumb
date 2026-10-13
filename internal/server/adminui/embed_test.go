package adminui

import (
	"os/exec"
	"testing"
)

func TestPanelLifecycle(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatal("node is required for deterministic admin UI verification")
	}
	out, err := exec.Command("node", "--test", "panel.test.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("admin UI lifecycle: %v\n%s", err, out)
	}
}
