// Package debug provides a tiny, globally-toggled debug logger.
//
// Debug output goes to stderr (so it never mixes with a command's stdout) and is
// silent unless enabled via the --debug flag or the BTR_DEBUG environment variable.
// It exists mainly as a learning aid: the logs narrate how a task definition is
// turned into an LLB graph and solved on buildkitd.
package debug

import (
	"fmt"
	"os"
)

var enabled bool

// SetEnabled turns debug logging on or off.
func SetEnabled(v bool) { enabled = v }

// Enabled reports whether debug logging is on.
func Enabled() bool { return enabled }

// Logf prints a debug line to stderr when debugging is enabled.
func Logf(format string, args ...any) {
	if !enabled {
		return
	}
	fmt.Fprintf(os.Stderr, "[debug] "+format+"\n", args...)
}
