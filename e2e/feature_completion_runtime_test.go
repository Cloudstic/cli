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
	runCompletionShellMatrix(t, completionScenario{
		name:  "root_commands",
		words: []string{"cloudstic", ""},
		assert: func(t *testing.T, out string) {
			assertCompletionContains(t, out, "backup", "prune", "restore")
		},
	})
}

func TestCLI_Feature_CompletionRuntime_BackupFlags(t *testing.T) {
	runCompletionShellMatrix(t, completionScenario{
		name:  "backup_flags",
		words: []string{"cloudstic", "backup", "-"},
		assert: func(t *testing.T, out string) {
			assertCompletionContains(t, out, "-profile", "-source", "-dry-run")
		},
	})
}

func TestCLI_Feature_CompletionRuntime_ProfileValues(t *testing.T) {
	runCompletionShellMatrix(t, completionScenario{
		name:  "profile_values",
		words: []string{"cloudstic", "backup", "-profile", ""},
		assert: func(t *testing.T, out string) {
			assertCompletionContains(t, out, "desktop", "documents")
		},
	})
}

func TestCLI_Feature_CompletionRuntime_StorePrefixes(t *testing.T) {
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
		shell := shell
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

func (rt completionRuntime) runBash(t *testing.T, words []string) string {
	t.Helper()
	var quoted []string
	for _, word := range words {
		quoted = append(quoted, shellQuote(word))
	}
	return runShell(t, "bash", rt.env, `
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
	return runShell(t, "fish", rt.env, `
source ("`+rt.bin+`" completion fish | psub)
complete --do-complete `+shellQuote(line)+`
`)
}

func (rt completionRuntime) runZsh(t *testing.T, words []string) string {
	t.Helper()
	scriptBody := `_cloudstic`
	switch {
	case len(words) >= 2 && words[1] == "":
		scriptBody = `
_describe() {
    local arrname="${@: -1}"
    eval "print -l -- \${${arrname}[@]}"
}
_arguments() { return 0 }
_cloudstic
`
	case len(words) >= 3 && words[1] == "backup" && words[2] == "":
		scriptBody = `
_describe() { return 0 }
_arguments() { print -l -- "$@" }
_cloudstic
`
	case len(words) >= 3 && words[1] == "backup" && words[2] == "-":
		scriptBody = `
_describe() { return 0 }
_arguments() { print -l -- "$@" }
_cloudstic
`
	case len(words) >= 4 && words[1] == "backup" && words[2] == "-profile" && words[3] == "":
		scriptBody = `
PREFIX=""
_cloudstic_query profile-names
`
	case len(words) >= 3 && words[1] == "-store" && words[2] == "":
		scriptBody = `
compadd() {
    local arg
    for arg in "$@"; do
        case "$arg" in
            -*) ;;
            *) print -r -- "$arg" ;;
        esac
    done
}
_cloudstic_store_prefixes
`
	}
	return runShell(t, "zsh", append(append([]string{}, rt.env...), zshHomeEnv(t)...), `
autoload -Uz compinit
compinit -i -d "$HOME/.zcompdump"
source <("`+rt.bin+`" completion zsh)
words=(`+zshWords(words)+`)
CURRENT=`+strconv.Itoa(len(words))+`
`+scriptBody+`
`)
}

func zshHomeEnv(t *testing.T) []string {
	t.Helper()
	tmp := t.TempDir()
	return []string{"HOME=" + tmp, "ZDOTDIR=" + tmp}
}

func zshWords(words []string) string {
	var quoted []string
	for _, word := range words {
		quoted = append(quoted, shellQuote(word))
	}
	return strings.Join(quoted, " ")
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
	cmd := exec.Command(shell, "-lc", script)
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
