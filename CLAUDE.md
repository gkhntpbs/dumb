@AGENTS.md

<!-- Claude Code imports AGENTS.md through the line above. The content lives in AGENTS.md —
     do not copy it here (copies drift). Add only Claude-specific notes below that line. -->

Windows development follows the isolated portable-toolchain workflow in
`docs/windows-worker.md`; retain the host's services and VMs, and never reboot
for worker tests. Interactive tray checks run in the signed-in desktop session.
