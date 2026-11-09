package app

import (
	"fmt"
	"os"
	"time"
)

// RunContext returns the per-run facts placed in front of a Turn's user input:
// the local date, the directory relative paths resolve against, and whether
// the session already holds earlier turns. They sit at the very end of the
// context, after the system prompt, the tool definitions and the
// per-installation environment, because they change most often. An empty
// workingDirectory means the process working directory.
func RunContext(workingDirectory string, now time.Time, hasHistory bool) string {
	if workingDirectory == "" {
		workingDirectory, _ = os.Getwd()
	}
	text := "<run_context>\nDate (local): " + now.Format(time.DateOnly)
	if workingDirectory != "" {
		text += fmt.Sprintf("\nWorking directory: %s", workingDirectory)
	}
	if hasHistory {
		text += "\nSession: earlier turns are saved but are not part of this conversation; use the history tool to read them."
	}
	return text + "\n</run_context>"
}
