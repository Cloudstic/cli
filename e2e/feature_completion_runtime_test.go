package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type completionRuntime struct {
	bin string
	env []string
}

type completionScenario struct {
	name   string
	words  []string
	assert func(t *testing.T, out string)
}

func TestCLI_Feature_CompletionRuntime_RootCommands(t *testing.T) {
	t.Parallel()
	runCompletionShellMatrix(t, completionScenario{
		name:  "root_commands",
		words: []string{"cloudstic", ""},
		assert: func(t *testing.T, out string) {
			assertCompletionContains(t, out, "backup", "prune", "restore")
		},
	})
}

func TestCLI_Feature_CompletionRuntime_BackupFlags(t *testing.T) {
	t.Parallel()
	runCompletionShellMatrix(t, completionScenario{
		name:  "backup_flags",
		words: []string{"cloudstic", "backup", "-"},
		assert: func(t *testing.T, out string) {
			assertCompletionContains(t, out, "-profile", "-source", "-dry-run")
		},
	})
}

func TestCLI_Feature_CompletionRuntime_GroupedSubcommandFlags(t *testing.T) {
	t.Parallel()
	runCompletionShellMatrix(t, completionScenario{
		name:  "grouped_subcommand_flags",
		words: []string{"cloudstic", "key", "passwd", "-"},
		assert: func(t *testing.T, out string) {
			assertCompletionContains(t, out, "-new-password")
		},
	})
}

func TestCLI_Feature_CompletionRuntime_ProfileValues(t *testing.T) {
	t.Parallel()
	runCompletionShellMatrix(t, completionScenario{
		name:  "profile_values",
		words: []string{"cloudstic", "backup", "-profile", ""},
		assert: func(t *testing.T, out string) {
			assertCompletionContains(t, out, "desktop", "documents")
		},
	})
}

func TestCLI_Feature_CompletionRuntime_StorePrefixes(t *testing.T) {
	t.Parallel()
	runCompletionShellMatrix(t, completionScenario{
		name:  "store_prefixes",
		words: []string{"cloudstic", "-store", ""},
		assert: func(t *testing.T, out string) {
			assertCompletionContains(t, out, "local:", "s3:", "b2:", "sftp://")
		},
	})
}

func runCompletionShellMatrix(t *testing.T, scenario completionScenario) {
	t.Helper()
	if !shouldRun(Hermetic) {
		t.Skip("skipping hermetic test")
	}

	rt := newCompletionRuntime(t)
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skipf("%s not installed", shell)
			}
			out := rt.runCompletion(t, shell, scenario.words)
			scenario.assert(t, out)
		})
	}
}

func (rt completionRuntime) runCompletion(t *testing.T, shell string, words []string) string {
	t.Helper()
	switch shell {
	case "bash":
		return rt.runBash(t, words)
	case "zsh":
		return rt.runZsh(t, words)
	case "fish":
		return rt.runFish(t, strings.Join(words, " "))
	default:
		t.Fatalf("unknown shell %q", shell)
		return ""
	}
}

func newCompletionRuntime(t *testing.T) completionRuntime {
	t.Helper()

	bin := buildBinary(t)
	profilesPath := writeCompletionProfilesFile(t)
	env := append(
		cleanEnv(),
		"PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CLOUDSTIC_PROFILES_FILE="+profilesPath,
	)
	return completionRuntime{bin: bin, env: env}
}

// pathPrelude re-prepends the freshly built binary's directory to PATH from
// inside the script. The generated completion scripts call the runtime query
// helper as a bare `cloudstic`, so any cloudstic installed elsewhere on PATH
// would answer instead of the build under test — and an older one silently
// returns nothing for `__complete`. Shell startup files (notably macOS'
// path_helper) reorder PATH, so setting it in the process environment alone is
// not enough to guarantee the build under test wins.
func (rt completionRuntime) pathPrelude(shell string) string {
	dir := shellQuote(filepath.Dir(rt.bin))
	if shell == "fish" {
		return "set -gx PATH " + dir + " $PATH\n"
	}
	return "export PATH=" + dir + "${PATH:+:$PATH}\n"
}

func (rt completionRuntime) runBash(t *testing.T, words []string) string {
	t.Helper()
	var quoted []string
	for _, word := range words {
		quoted = append(quoted, shellQuote(word))
	}
	return runShell(t, "bash", rt.env, rt.pathPrelude("bash")+`
completion_file="$(mktemp)"
"`+rt.bin+`" completion bash > "$completion_file"
source "$completion_file"
_init_completion() {
    words=("${COMP_WORDS[@]}")
    cword=$COMP_CWORD
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
}
COMP_WORDS=(`+strings.Join(quoted, " ")+`)
COMP_CWORD=`+strconv.Itoa(len(words)-1)+`
_cloudstic
printf '%s\n' "${COMPREPLY[@]}"
`)
}

func (rt completionRuntime) runFish(t *testing.T, line string) string {
	t.Helper()
	return runShell(t, "fish", rt.env, rt.pathPrelude("fish")+`
source ("`+rt.bin+`" completion fish | psub)
complete --do-complete `+shellQuote(line)+`
`)
}

// runZsh drives a real interactive zsh through a pseudo-terminal and captures
// what pressing TAB actually offers.
//
// Calling _cloudstic directly with stubbed _describe/_arguments would be
// simpler, but it would only assert which specification strings the script
// passes along, not that zsh's completion system does anything with them. That
// blind spot hid a bug in which every subcommand offered nothing at all:
// _arguments completes relative to the command word, and the script never
// re-based $words on the subcommand before calling it.
func (rt completionRuntime) runZsh(t *testing.T, words []string) string {
	t.Helper()

	home := t.TempDir()
	rc := rt.pathPrelude("zsh") + `
export CLOUDSTIC_PROFILES_FILE=` + shellQuote(envValue(rt.env, "CLOUDSTIC_PROFILES_FILE")) + `
autoload -Uz compinit
compinit -C -d ` + shellQuote(filepath.Join(home, ".zcompdump")) + `
source <(` + shellQuote(rt.bin) + ` completion zsh)
PS1=';;;'
`
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}

	// Two TABs: the first inserts any unambiguous common prefix, the second
	// lists whatever candidates remain.
	line := strings.Join(words, " ") + "\t\t"
	return runShell(t, "zsh", rt.env, `
emulate -L zsh
zmodload zsh/zpty

# Read until the pty has been quiet for a short while: completion runs
# asynchronously inside the child shell, so there is no output marker to wait
# for.
drain() {
    local chunk all=
    local -i idle=0
    while (( idle < 40 )); do
        if zpty -rt comp chunk 2>/dev/null; then
            all+=$chunk
            idle=0
        else
            (( idle++ ))
            sleep 0.05
        fi
    done
    print -rn -- "$all"
}

# Disable global startup files so the host's /etc/zshrc cannot initialize
# completion before our isolated test configuration. In particular, CI images
# may run compinit interactively and abort while waiting for a security prompt.
zpty -b comp `+shellQuote("HOME="+home+" ZDOTDIR="+home+" zsh -d -i")+`
sleep 2
drain >/dev/null
zpty -w -n comp `+shellQuote(line)+`
sleep 1
drain
zpty -d comp
`)
}

// envValue returns the value of key in a KEY=VALUE environment slice.
func envValue(env []string, key string) string {
	for _, kv := range env {
		if value, ok := strings.CutPrefix(kv, key+"="); ok {
			return value
		}
	}
	return ""
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func assertCompletionContains(t *testing.T, out string, values ...string) {
	t.Helper()
	for _, want := range values {
		if !strings.Contains(out, want) {
			t.Fatalf("completion missing %q, got:\n%s", want, out)
		}
	}
}

func runShell(t *testing.T, shell string, env []string, script string) string {
	t.Helper()
	// Deliberately not a login shell: startup files are user-specific (and on
	// macOS path_helper rewrites PATH), which makes the harness depend on the
	// developer's machine rather than on the build under test.
	cmd := exec.Command(shell, "-c", script)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s script failed: %v\n%s", shell, err, out)
	}
	return string(out)
}

func writeCompletionProfilesFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.yaml")
	content := `version: 1
profiles:
  documents:
    source: local:/tmp/documents
  desktop:
    source: local:/tmp/desktop
auth:
  google-work:
    provider: google
stores:
  primary:
    uri: local:/tmp/store
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}
