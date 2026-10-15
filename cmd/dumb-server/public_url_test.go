package main

import "testing"

func TestPublicURLFromEnvironment(t *testing.T) {
	t.Setenv("DUMB_PUBLIC_URL", "")
	if u, err := publicURLFromEnvironment(); err != nil || u != "" {
		t.Fatalf("unset: %q %v", u, err)
	}
	t.Setenv("DUMB_PUBLIC_URL", "https://dumb.example/")
	if u, err := publicURLFromEnvironment(); err != nil || u != "https://dumb.example" {
		t.Fatalf("valid: %q %v", u, err)
	}
	for _, bad := range []string{"http://dumb.example", "dumb.example", "https://user@dumb.example", "https://dumb.example/?x=1"} {
		t.Setenv("DUMB_PUBLIC_URL", bad)
		if u, err := publicURLFromEnvironment(); err == nil {
			t.Errorf("%q accepted as %q", bad, u)
		}
	}
}
