package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// captureStdio runs fn with os.Stdout and os.Stderr redirected into buffers so a
// test can assert on what the CLI prints.
func captureStdio(t *testing.T, fn func() error) (string, string, error) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	runErr := fn()
	os.Stdout, os.Stderr = oldOut, oldErr
	outW.Close()
	errW.Close()

	var outBuf, errBuf bytes.Buffer
	io.Copy(&outBuf, outR)
	io.Copy(&errBuf, errR)
	outR.Close()
	errR.Close()
	return outBuf.String(), errBuf.String(), runErr
}

// documentedFlags pulls the flags out of the usage text, which is the part of
// the CLI a user reads. It is the reference the completion scripts must cover.
func documentedFlags(t *testing.T) []string {
	t.Helper()
	_, stderr, err := captureStdio(t, func() error { return run([]string{"--help"}) })
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	seen := map[string]bool{}
	var flags []string
	for _, field := range regexp.MustCompile(`--[a-z][a-z0-9-]+`).FindAllString(stderr, -1) {
		if !seen[field] {
			seen[field] = true
			flags = append(flags, field)
		}
	}
	if len(flags) < 5 {
		t.Fatalf("usage text only yielded %d flags: %v", len(flags), flags)
	}
	return flags
}

// writeScript drops a generated completion script in a temp file so a real
// shell can parse it.
func writeScript(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestCompletionScriptsCoverUsage(t *testing.T) {
	flags := documentedFlags(t)
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			stdout, stderr, err := captureStdio(t, func() error { return run([]string{"completion", shell}) })
			if err != nil {
				t.Fatalf("completion %s: %v (stderr %q)", shell, err, stderr)
			}
			if stdout == "" {
				t.Fatalf("completion %s printed nothing", shell)
			}
			if !strings.HasSuffix(stdout, "\n") {
				t.Errorf("completion %s does not end with a newline", shell)
			}
			for _, flag := range flags {
				wanted := flag
				if shell == "fish" {
					// fish spells long flags as -l name, not --name.
					wanted = "-l " + strings.TrimPrefix(flag, "--")
				}
				if !strings.Contains(stdout, wanted) {
					t.Errorf("%s completion does not offer %s, which the usage text documents", shell, wanted)
				}
			}
			for _, cmd := range []string{"install", "list", "completion", "version", "help"} {
				if !strings.Contains(stdout, cmd) {
					t.Errorf("%s completion does not mention the %s command", shell, cmd)
				}
			}
		})
	}
}

func TestCompletionRejectsUnknownShell(t *testing.T) {
	stdout, stderr, err := captureStdio(t, func() error { return run([]string{"completion", "tcsh"}) })
	if err == nil {
		t.Fatal("completion tcsh: expected an error")
	}
	if !strings.Contains(err.Error(), "tcsh") || !strings.Contains(err.Error(), "bash") {
		t.Errorf("error %q should name the offending shell and the supported ones", err)
	}
	if stdout != "" {
		t.Errorf("printed %q on stdout for an unsupported shell", stdout)
	}
	if stderr != "" {
		t.Errorf("the command should return the error instead of printing it twice, got %q", stderr)
	}
}

func TestCompletionRequiresOneShell(t *testing.T) {
	for _, args := range [][]string{{"completion"}, {"completion", "bash", "zsh"}} {
		if _, _, err := captureStdio(t, func() error { return run(args) }); err == nil {
			t.Errorf("run(%v): expected a usage error", args)
		}
	}
}

func TestBashCompletionScriptParses(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	stdout, _, err := captureStdio(t, func() error { return run([]string{"completion", "bash"}) })
	if err != nil {
		t.Fatalf("completion bash: %v", err)
	}
	script := writeScript(t, "ghfetch.bash", stdout)
	if out, err := exec.Command(bash, "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("bash -n rejected the script: %v\n%s", err, out)
	}
}

func TestZshCompletionScriptParses(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	stdout, _, err := captureStdio(t, func() error { return run([]string{"completion", "zsh"}) })
	if err != nil {
		t.Fatalf("completion zsh: %v", err)
	}
	script := writeScript(t, "_ghfetch", stdout)
	if out, err := exec.Command(zsh, "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("zsh -n rejected the script: %v\n%s", err, out)
	}
}

func TestFishCompletionScriptParses(t *testing.T) {
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish not installed")
	}
	stdout, _, err := captureStdio(t, func() error { return run([]string{"completion", "fish"}) })
	if err != nil {
		t.Fatalf("completion fish: %v", err)
	}
	script := writeScript(t, "ghfetch.fish", stdout)
	if out, err := exec.Command(fish, "--no-execute", script).CombinedOutput(); err != nil {
		t.Fatalf("fish --no-execute rejected the script: %v\n%s", err, out)
	}
}

// TestBashCompletionBehaviour drives the generated function the way bash does.
// Parsing the script is not enough: a script that parses but offers nothing
// still passes -n, and that is exactly the failure a user would hit.
func TestBashCompletionBehaviour(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	stdout, _, err := captureStdio(t, func() error { return run([]string{"completion", "bash"}) })
	if err != nil {
		t.Fatalf("completion bash: %v", err)
	}
	script := writeScript(t, "ghfetch.bash", stdout)

	cases := []struct {
		name  string
		words string // COMP_WORDS, the last word being the one completed
		want  []string
	}{
		{"subcommand", "ghfetch co", []string{"completion"}},
		{"install flag", "ghfetch install --require", []string{"--require-checksum", "--require-attestation"}},
		{"list flag", "ghfetch list --json", []string{"--json"}},
		{"shell name", "ghfetch completion z", []string{"zsh"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// COMP_CWORD is one less than the number of words: the cursor sits
			// on the last word.
			words := strings.Fields(tc.words)
			prog := "source \"$1\"\n" +
				"COMP_WORDS=(" + strings.Join(words, " ") + ")\n" +
				"COMP_CWORD=$((" + strconv.Itoa(len(words)) + " - 1))\n" +
				"_ghfetch\n" +
				"printf '%s\\n' \"${COMPREPLY[@]}\"\n"
			cmd := exec.Command(bash, "-c", prog, "bash", script)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("running the completion function: %v\n%s", err, out)
			}
			got := strings.Fields(string(out))
			for _, want := range tc.want {
				if !contains(got, want) {
					t.Errorf("completing %q offered %v, missing %q", tc.words, got, want)
				}
			}
		})
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
