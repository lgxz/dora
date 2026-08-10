package main

import "testing"

func TestVersionString(t *testing.T) {
	oldVersion, oldCommit, oldDate := version, commit, date
	t.Cleanup(func() {
		version, commit, date = oldVersion, oldCommit, oldDate
	})
	version, commit, date = "1.2.3", "abc123", "2026-08-10T00:00:00Z"

	want := "dora 1.2.3 (commit abc123, built 2026-08-10T00:00:00Z)"
	if got := versionString(); got != want {
		t.Fatalf("version = %q, want %q", got, want)
	}
}
