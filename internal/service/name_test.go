package service

import (
	"encoding/xml"
	"strings"
	"testing"
)

// An explicitly configured display name survives each service format intact,
// including characters each format treats specially.
func TestWorkerNameInEveryServiceFormat(t *testing.T) {
	const name = "G\u00f6khan's \"Pi\" 100% $HOME & <lab>"
	for _, goos := range []string{"darwin", "linux", "windows"} {
		c, _ := cfg(t, goos)
		c.Env["DUMB_WORKER_NAME"] = name
		switch goos {
		case "darwin":
			var plist struct {
				Dict struct {
					Inner []struct {
						Strings []string `xml:"string"`
					} `xml:"dict"`
				} `xml:"dict"`
			}
			if err := xml.Unmarshal([]byte(Plist(c)), &plist); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, d := range plist.Dict.Inner {
				for _, s := range d.Strings {
					found = found || s == name
				}
			}
			if !found {
				t.Errorf("plist lacks the name:\n%s", Plist(c))
			}
		case "linux":
			want := `Environment="DUMB_WORKER_NAME=G` + "\u00f6" + `khan's \"Pi\" 100%% $$HOME & <lab>"`
			if u := SystemdUnit(c); !strings.Contains(u, want) {
				t.Errorf("unit lacks %s:\n%s", want, u)
			}
		case "windows":
			bin := WindowsCreateArgs(c)[3]
			if want := ` -name "G` + "\u00f6" + `khan's \"Pi\" 100% $HOME & <lab>"`; !strings.Contains(bin, want) {
				t.Errorf("binPath lacks %s: %s", want, bin)
			}
		}
	}
}
