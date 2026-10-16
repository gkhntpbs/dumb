package main

import "testing"

func TestMacApplicationPreservesBundleSignatureDuringUpdates(t *testing.T) {
	for _, test := range []struct {
		goos, path string
		allowed    bool
	}{
		{"darwin", "/Applications/DUMB Worker.app/Contents/MacOS/dumb-worker", false},
		{"darwin", "/usr/local/bin/dumb-worker", true},
		{"linux", "/opt/dumb-worker", true},
		{"windows", "C:/DUMB/dumb-worker.exe", true},
	} {
		if got := binaryUpdatesAllowed(test.goos, test.path); got != test.allowed {
			t.Fatalf("%s %s allowed=%v", test.goos, test.path, got)
		}
	}
}
