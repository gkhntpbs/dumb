// Package service generates and installs the OS service that keeps dumb-worker
// running: a launchd user agent on macOS, a systemd unit on Linux and a Windows
// service. Every command goes through a Runner so tests never touch the system.
package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// DefaultLabel names the service on every OS.
const DefaultLabel = "dumb-worker"

// DefaultUser is the dedicated account a Linux system unit runs as. Create it
// before installing: see SystemUserHelp.
const DefaultUser = "dumb"

// SystemUserHelp creates the account and gives it the binary's directory, which
// self-update must be able to write.
const SystemUserHelp = `sudo useradd --system --user-group --home-dir /var/lib/dumb-worker --create-home --shell /usr/sbin/nologin dumb
sudo install -d -o dumb -g dumb -m 0755 /opt/dumb-worker      # binary dir: dumb owns it (self-update)
sudo install -o dumb -g dumb -m 0755 dumb-worker /opt/dumb-worker/
sudo install -d -o dumb -g dumb -m 0700 /etc/dumb-worker
sudo install -o dumb -g dumb -m 0600 token /etc/dumb-worker/token`

// Runner runs one command to completion.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
}

type Config struct {
	GOOS   string
	Label  string            // default DefaultLabel; launchd label, systemd unit and Windows service name
	Binary string            // absolute path to dumb-worker
	Env    map[string]string // configuration passed as environment (DUMB_*)
	LogDir string            // macOS stdout/stderr logs; default ~/Library/Logs
	Home   string
	UID    int
	System bool   // Linux: a system unit instead of a user unit
	User   string // Linux system unit: the account it runs as; default DefaultUser
	Root   string // prefix for absolute system paths (tests); default "/"
	Runner Runner
}

func (c *Config) label() string {
	if c.Label == "" {
		return DefaultLabel
	}
	return c.Label
}

func (c *Config) envKeys() []string {
	keys := make([]string, 0, len(c.Env))
	for k := range c.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Path is where the service definition is written; "" on Windows (no file).
func Path(c Config) string {
	switch c.GOOS {
	case "darwin":
		return filepath.Join(c.Home, "Library", "LaunchAgents", c.label()+".plist")
	case "linux":
		if c.System {
			root := c.Root
			if root == "" {
				root = "/"
			}
			return filepath.Join(root, "etc", "systemd", "system", c.label()+".service")
		}
		return filepath.Join(c.Home, ".config", "systemd", "user", c.label()+".service")
	}
	return ""
}

func xmlText(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Plist is the launchd user agent.
func Plist(c Config) string {
	logDir := c.LogDir
	if logDir == "" {
		logDir = filepath.Join(c.Home, "Library", "Logs")
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	kv := func(k, v string) { fmt.Fprintf(&b, "\t<key>%s</key>\n\t<string>%s</string>\n", k, xmlText(v)) }
	kv("Label", c.label())
	fmt.Fprintf(&b, "\t<key>ProgramArguments</key>\n\t<array>\n\t\t<string>%s</string>\n\t\t<string>run</string>\n\t</array>\n", xmlText(c.Binary))
	// KeepAlive restarts every exit, including 4 (restart after an update or
	// rollback). launchd respawns at once when the job ran at least
	// ThrottleInterval seconds, otherwise after the rest of it. The worker no
	// longer exits on 426, so an outdated worker does not spin.
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n\t<key>KeepAlive</key>\n\t<true/>\n")
	b.WriteString("\t<key>ThrottleInterval</key>\n\t<integer>10</integer>\n")
	if len(c.Env) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, k := range c.envKeys() {
			fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", xmlText(k), xmlText(c.Env[k]))
		}
		b.WriteString("\t</dict>\n")
	}
	kv("StandardOutPath", filepath.Join(logDir, c.label()+".log"))
	kv("StandardErrorPath", filepath.Join(logDir, c.label()+".log"))
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// systemdQuote quotes a value for an Environment= or ExecStart= word.
func systemdQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$")
	return `"` + r.Replace(s) + `"`
}

// SystemdUnit is the systemd service unit.
func SystemdUnit(c Config) string {
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=DUMB worker (Distributed User-owned Model Backend)\n")
	b.WriteString("Wants=network-online.target\nAfter=network-online.target\n\n[Service]\n")
	fmt.Fprintf(&b, "ExecStart=%s run\n", systemdQuote(c.Binary))
	for _, k := range c.envKeys() {
		fmt.Fprintf(&b, "Environment=%s\n", systemdQuote(k+"="+c.Env[k]))
	}
	// Restart=always covers exit code 4 (restart after an update or rollback).
	b.WriteString("Restart=always\nRestartSec=5\n")
	if c.System {
		user := c.User
		if user == "" {
			user = DefaultUser
		}
		fmt.Fprintf(&b, "User=%s\nGroup=%s\n", user, user)
		// Hardening. The worker only needs outbound TCP, its token file and
		// write access to its own directory: self-update renames files next to
		// the binary (<exe>.new, .prev, .update.json, .skip.json, .bad).
		b.WriteString("NoNewPrivileges=yes\nProtectSystem=strict\n")
		// No environment expansion here, so "$" is not escaped; specifiers ("%") are.
		dir := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace(filepath.Dir(c.Binary))
		fmt.Fprintf(&b, "ReadWritePaths=\"%s\"\n", dir)
		b.WriteString("StateDirectory=dumb-worker\n" +
			"ProtectHome=read-only\nPrivateTmp=yes\nPrivateDevices=yes\n" +
			"ProtectKernelTunables=yes\nProtectKernelModules=yes\nProtectKernelLogs=yes\nProtectControlGroups=yes\n" +
			"ProtectClock=yes\nProtectHostname=yes\nRestrictSUIDSGID=yes\nRestrictRealtime=yes\nRestrictNamespaces=yes\n" +
			"LockPersonality=yes\nMemoryDenyWriteExecute=yes\nSystemCallArchitectures=native\n" +
			"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX\nCapabilityBoundingSet=\nAmbientCapabilities=\nUMask=0077\n")
	}
	b.WriteString("\n[Install]\n")
	if c.System {
		b.WriteString("WantedBy=multi-user.target\n")
	} else {
		b.WriteString("WantedBy=default.target\n")
	}
	return b.String()
}

// WindowsCreateArgs are the sc.exe arguments that register the service. sc.exe
// cannot set environment variables, so configuration travels as flags.
func WindowsCreateArgs(c Config) []string {
	bin := []string{`"` + c.Binary + `"`, "run"}
	for _, k := range c.envKeys() {
		if f, ok := envFlags[k]; ok {
			bin = append(bin, "-"+f, `"`+c.Env[k]+`"`)
		}
	}
	return []string{"create", c.label(), "binPath=", strings.Join(bin, " "), "start=", "auto", "DisplayName=", "DUMB worker"}
}

// WindowsFailureArgs make the Service Control Manager restart the worker
// after it exits: at once, then after 5s and 60s; the count resets daily.
func WindowsFailureArgs(c Config) []string {
	return []string{"failure", c.label(), "reset=", "86400", "actions=", "restart/0/restart/5000/restart/60000"}
}

// envFlags maps configuration variables to dumb-worker flags.
var envFlags = map[string]string{
	"DUMB_SERVER": "server", "DUMB_TOKEN_FILE": "token-file", "DUMB_MODELS": "models",
	"DUMB_CAPACITY": "capacity", "DUMB_OLLAMA": "ollama", "DUMB_CHANNEL": "channel",
	"DUMB_UPDATE_URL": "update-url",
}

// Print returns the service definition as it would be installed.
func Print(c Config) (string, error) {
	switch c.GOOS {
	case "darwin":
		return Plist(c), nil
	case "linux":
		return SystemdUnit(c), nil
	case "windows":
		args := WindowsCreateArgs(c)
		for i, a := range args {
			if strings.ContainsAny(a, " \"") {
				args[i] = strconv.Quote(a)
			}
		}
		return "sc.exe " + strings.Join(args, " ") + "\n", nil
	}
	return "", fmt.Errorf("service: unsupported OS %s", c.GOOS)
}

func (c *Config) write(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (c *Config) remove(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (c *Config) systemctl() []string {
	if c.System {
		return []string{"systemctl"}
	}
	return []string{"systemctl", "--user"}
}

func (c *Config) run(ctx context.Context, argv []string) error {
	return c.Runner.Run(ctx, argv[0], argv[1:]...)
}

// Install writes the service definition and starts the service.
func Install(ctx context.Context, c Config) error {
	if !filepath.IsAbs(c.Binary) {
		return fmt.Errorf("service: binary path %q must be absolute", c.Binary)
	}
	switch c.GOOS {
	case "darwin":
		p, domain := Path(c), "gui/"+strconv.Itoa(c.UID)
		if err := c.write(p, []byte(Plist(c))); err != nil {
			return err
		}
		// A previous version may still be loaded; bootstrap fails if it is.
		c.run(ctx, []string{"launchctl", "bootout", domain + "/" + c.label()})
		return c.run(ctx, []string{"launchctl", "bootstrap", domain, p})
	case "linux":
		if err := c.write(Path(c), []byte(SystemdUnit(c))); err != nil {
			return err
		}
		if err := c.run(ctx, append(c.systemctl(), "daemon-reload")); err != nil {
			return err
		}
		return c.run(ctx, append(c.systemctl(), "enable", "--now", c.label()+".service"))
	case "windows":
		if err := c.run(ctx, append([]string{"sc.exe"}, WindowsCreateArgs(c)...)); err != nil {
			return err
		}
		// Restart on any non-zero exit, including 4 (after an update or rollback).
		if err := c.run(ctx, append([]string{"sc.exe"}, WindowsFailureArgs(c)...)); err != nil {
			return err
		}
		return c.run(ctx, []string{"sc.exe", "start", c.label()})
	}
	return fmt.Errorf("service: unsupported OS %s", c.GOOS)
}

// Uninstall stops the service and removes its definition.
func Uninstall(ctx context.Context, c Config) error {
	switch c.GOOS {
	case "darwin":
		c.run(ctx, []string{"launchctl", "bootout", "gui/" + strconv.Itoa(c.UID) + "/" + c.label()})
		return c.remove(Path(c))
	case "linux":
		c.run(ctx, append(c.systemctl(), "disable", "--now", c.label()+".service"))
		if err := c.remove(Path(c)); err != nil {
			return err
		}
		return c.run(ctx, append(c.systemctl(), "daemon-reload"))
	case "windows":
		c.run(ctx, []string{"sc.exe", "stop", c.label()})
		return c.run(ctx, []string{"sc.exe", "delete", c.label()})
	}
	return fmt.Errorf("service: unsupported OS %s", c.GOOS)
}
