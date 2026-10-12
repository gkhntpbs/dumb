package service

import (
	"context"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type fakeRunner struct{ runs []string }

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	f.runs = append(f.runs, strings.Join(append([]string{name}, args...), " "))
	return nil
}

func cfg(t *testing.T, goos string) (Config, *fakeRunner) {
	r := &fakeRunner{}
	return Config{
		GOOS: goos, Binary: "/opt/dumb & co/dumb-worker", Home: t.TempDir(), UID: 501, Root: t.TempDir(), Runner: r,
		Env: map[string]string{"DUMB_SERVER": "https://dumb.example.invalid", "DUMB_MODELS": "a:1b,b<2>", "DUMB_TOKEN_FILE": "/home/u/.dumb/token"},
	}, r
}

func TestPlistIsValidXML(t *testing.T) {
	c, _ := cfg(t, "darwin")
	p := Plist(c)
	dec := xml.NewDecoder(strings.NewReader(p))
	dec.Strict = true
	var strs []string
	inString := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid XML: %v\n%s", err, p)
		}
		switch v := tok.(type) {
		case xml.StartElement:
			inString = v.Name.Local == "string" || v.Name.Local == "key"
		case xml.CharData:
			if inString {
				strs = append(strs, string(v))
			}
		case xml.EndElement:
			inString = false
		}
	}
	for _, want := range []string{"Label", "dumb-worker", "ProgramArguments", "/opt/dumb & co/dumb-worker", "run",
		"DUMB_MODELS", "a:1b,b<2>", "StandardOutPath", filepath.Join(c.Home, "Library", "Logs", "dumb-worker.log")} {
		if !slices.Contains(strs, want) {
			t.Errorf("plist lacks %q: %q", want, strs)
		}
	}
	for _, want := range []string{"<key>RunAtLoad</key>\n\t<true/>", "<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>", "<key>ThrottleInterval</key>\n\t<integer>10</integer>", "StandardErrorPath"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %q", want)
		}
	}
}

func TestSystemdUnit(t *testing.T) {
	c, _ := cfg(t, "linux")
	u := SystemdUnit(c)
	for _, want := range []string{
		`ExecStart="/opt/dumb & co/dumb-worker" run`,
		`Environment="DUMB_MODELS=a:1b,b<2>"`,
		`Environment="DUMB_SERVER=https://dumb.example.invalid"`,
		"Restart=always", "After=network-online.target", "WantedBy=default.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
	c.System = true
	if u := SystemdUnit(c); !strings.Contains(u, "WantedBy=multi-user.target") {
		t.Errorf("system unit:\n%s", u)
	}
}

func TestWindowsCreateArgs(t *testing.T) {
	c, _ := cfg(t, "windows")
	c.Binary = `C:\Program Files\DUMB\dumb-worker.exe`
	got := WindowsCreateArgs(c)
	want := []string{"create", "dumb-worker", "binPath=",
		`"C:\Program Files\DUMB\dumb-worker.exe" run -models "a:1b,b<2>" -server "https://dumb.example.invalid" -token-file "/home/u/.dumb/token"`,
		"start=", "auto", "DisplayName=", "DUMB worker"}
	if !slices.Equal(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestInstallUninstall(t *testing.T) {
	cases := []struct {
		goos      string
		system    bool
		install   []string
		uninstall []string
	}{
		{"darwin", false,
			[]string{"launchctl bootout gui/501/dumb-worker", "launchctl bootstrap gui/501 {path}"},
			[]string{"launchctl bootout gui/501/dumb-worker"}},
		{"linux", false,
			[]string{"systemctl --user daemon-reload", "systemctl --user enable --now dumb-worker.service"},
			[]string{"systemctl --user disable --now dumb-worker.service", "systemctl --user daemon-reload"}},
		{"linux", true,
			[]string{"systemctl daemon-reload", "systemctl enable --now dumb-worker.service"},
			[]string{"systemctl disable --now dumb-worker.service", "systemctl daemon-reload"}},
		{"windows", false,
			[]string{"sc.exe create dumb-worker binPath= \"/opt/dumb & co/dumb-worker\" run -models \"a:1b,b<2>\" -server \"https://dumb.example.invalid\" -token-file \"/home/u/.dumb/token\" start= auto DisplayName= DUMB worker",
				"sc.exe failure dumb-worker reset= 86400 actions= restart/0/restart/5000/restart/60000", "sc.exe start dumb-worker"},
			[]string{"sc.exe stop dumb-worker", "sc.exe delete dumb-worker"}},
	}
	for _, tc := range cases {
		c, r := cfg(t, tc.goos)
		c.System = tc.system
		p := Path(c)
		if err := Install(context.Background(), c); err != nil {
			t.Fatalf("%s: %v", tc.goos, err)
		}
		want := slices.Clone(tc.install)
		for i := range want {
			want[i] = strings.ReplaceAll(want[i], "{path}", p)
		}
		if !slices.Equal(r.runs, want) {
			t.Errorf("%s install: %q\nwant %q", tc.goos, r.runs, want)
		}
		if p != "" {
			if !strings.HasPrefix(p, c.Home) && !strings.HasPrefix(p, c.Root) {
				t.Fatalf("%s: path %s escapes the test dirs", tc.goos, p)
			}
			b, err := os.ReadFile(p)
			if err != nil || !strings.Contains(string(b), "dumb-worker") {
				t.Errorf("%s: file %s: %v", tc.goos, p, err)
			}
		}
		r.runs = nil
		if err := Uninstall(context.Background(), c); err != nil {
			t.Fatalf("%s uninstall: %v", tc.goos, err)
		}
		if !slices.Equal(r.runs, tc.uninstall) {
			t.Errorf("%s uninstall: %q\nwant %q", tc.goos, r.runs, tc.uninstall)
		}
		if p != "" {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Errorf("%s: %s not removed", tc.goos, p)
			}
		}
	}
	if err := Install(context.Background(), Config{GOOS: "darwin", Binary: "dumb-worker"}); err == nil {
		t.Error("relative binary accepted")
	}
}

func TestPaths(t *testing.T) {
	c := Config{GOOS: "darwin", Home: "/Users/u"}
	if p := Path(c); p != "/Users/u/Library/LaunchAgents/dumb-worker.plist" {
		t.Error(p)
	}
	c.GOOS = "linux"
	if p := Path(c); p != "/Users/u/.config/systemd/user/dumb-worker.service" {
		t.Error(p)
	}
	c.System = true
	if p := Path(c); p != "/etc/systemd/system/dumb-worker.service" {
		t.Error(p)
	}
}

// TestSystemdSystemUnitHardening: the system unit runs as a dedicated user with
// hardening that still lets self-update write next to the binary.
func TestSystemdSystemUnitHardening(t *testing.T) {
	c, _ := cfg(t, "linux")
	user := SystemdUnit(c)
	for _, not := range []string{"User=", "ProtectSystem=", "NoNewPrivileges="} {
		if strings.Contains(user, not) {
			t.Errorf("user unit must not carry %q (no privileges to drop):\n%s", not, user)
		}
	}
	c.System = true
	u := SystemdUnit(c)
	svc := u[strings.Index(u, "[Service]"):strings.Index(u, "[Install]")]
	for _, want := range []string{
		"User=dumb", "Group=dumb", "NoNewPrivileges=yes", "ProtectSystem=strict",
		`ReadWritePaths="/opt/dumb & co"`, "PrivateTmp=yes", "ProtectHome=read-only", "PrivateDevices=yes",
		"CapabilityBoundingSet=\n", "RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX", "Restart=always",
	} {
		if !strings.Contains(svc, want) {
			t.Errorf("[Service] lacks %q:\n%s", want, svc)
		}
	}
	// Every line in [Service] is a Key=Value directive.
	for _, l := range strings.Split(strings.TrimSpace(svc), "\n")[1:] {
		if k, _, ok := strings.Cut(l, "="); !ok || k == "" || strings.ContainsAny(k, " \t") {
			t.Errorf("bad directive %q", l)
		}
	}
	c.User = "svc-dumb"
	c.Binary = "/srv/100%/dumb-worker"
	u = SystemdUnit(c)
	if !strings.Contains(u, "User=svc-dumb\nGroup=svc-dumb") || !strings.Contains(u, `ReadWritePaths="/srv/100%%"`) {
		t.Errorf("custom user / escaped path:\n%s", u)
	}
	if !strings.Contains(SystemUserHelp, "useradd --system") || !strings.Contains(SystemUserHelp, "-o dumb") {
		t.Error("SystemUserHelp does not create the dumb user")
	}
}
