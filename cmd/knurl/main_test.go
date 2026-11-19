package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunCLIStub(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "no args prints help",
			args:       nil,
			wantCode:   0,
			wantStdout: "Usage:",
		},
		{
			name:       "help flag",
			args:       []string{"--help"},
			wantCode:   0,
			wantStdout: "knurl — Tools your agents forge",
		},
		{
			name:       "short help flag",
			args:       []string{"-h"},
			wantCode:   0,
			wantStdout: "Usage:",
		},
		{
			name:       "help subcommand",
			args:       []string{"help"},
			wantCode:   0,
			wantStdout: "Usage:",
		},
		{
			name:       "version flag",
			args:       []string{"--version"},
			wantCode:   0,
			wantStdout: "knurl " + version,
		},
		{
			name:       "short version flag",
			args:       []string{"-v"},
			wantCode:   0,
			wantStdout: "knurl " + version,
		},
		{
			name:       "version subcommand",
			args:       []string{"version"},
			wantCode:   0,
			wantStdout: "knurl " + version,
		},
		{
			name:       "unknown argument exits 2",
			args:       []string{"--bogus"},
			wantCode:   2,
			wantStderr: "unknown command or flag",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := run(tc.args, &stdout, &stderr)
			if got != tc.wantCode {
				t.Fatalf("run(%v) = %d, want %d (stderr=%q)", tc.args, got, tc.wantCode, stderr.String())
			}
			if tc.wantStdout != "" && !strings.Contains(stdout.String(), tc.wantStdout) {
				t.Errorf("stdout %q does not contain %q", stdout.String(), tc.wantStdout)
			}
			if tc.wantStderr != "" && !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr %q does not contain %q", stderr.String(), tc.wantStderr)
			}
		})
	}
}
