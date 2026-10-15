package runner

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// Restricted mode is a safety boundary. Report an upgrade requirement rather
// than silently dropping it on an older CLI. This runs on the execution host.
func CheckRestrictedClaude(ctx context.Context, command []string) error {
	if len(command) == 0 || filepath.Base(command[0]) != "claude" || !slices.Contains(command, "--restricted") {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, command[0], "--version").Output()
	if err == nil {
		match := regexp.MustCompile(`\b([0-9]+)\.([0-9]+)\.([0-9]+)\b`).FindStringSubmatch(string(output))
		if len(match) == 4 {
			major, _ := strconv.Atoi(match[1])
			minor, _ := strconv.Atoi(match[2])
			patch, _ := strconv.Atoi(match[3])
			if major > 2 || major == 2 && (minor > 1 || minor == 1 && patch >= 248) {
				return nil
			}
		}
	}
	return fmt.Errorf("Claude Code 2.1.248 or later is required for restricted agent checks; run claude update on the execution host")
}
