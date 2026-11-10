package app

import (
	"fmt"
	"os"
	"time"
)

// RunContext returns the per-run facts placed in front of a Turn's user input:
// the local date, the directory relative paths resolve against, and how many
// turns the session already holds. They sit at the very end of the context,
// after the system prompt, the tool definitions and the per-installation
// environment, because they change most often. An empty workingDirectory means
// the process working directory.
//
// savedTurns is the number of turns already saved in the session, counted the
// way the history tool counts them; turn IDs run from 1 in saving order, so
// the latest turn's ID equals the count. A negative value means unknown and
// leaves the session line out.
func RunContext(workingDirectory string, now time.Time, savedTurns int) string {
	if workingDirectory == "" {
		workingDirectory, _ = os.Getwd()
	}
	text := "<run_context>\nDate (local): " + now.Format(time.DateOnly)
	if workingDirectory != "" {
		text += fmt.Sprintf("\nWorking directory: %s", workingDirectory)
	}
	switch {
	case savedTurns == 0:
		text += "\nSession: new, no earlier turns."
	case savedTurns == 1:
		text += "\nSession: 1 earlier turn is saved but is not part of this conversation; use the history tool to read it (turn 1)."
	case savedTurns > 1:
		text += fmt.Sprintf("\nSession: %d earlier turns are saved but are not part of this conversation; use the history tool to read them (latest: turn %d).", savedTurns, savedTurns)
	}
	return text + "\n</run_context>"
}
