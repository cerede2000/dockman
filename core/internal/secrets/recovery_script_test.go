package secrets

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The recovery script is the one thing that makes an encrypted stack portable:
// with the ciphertext, the age identity and a host carrying only Docker and
// SOPS, it has to bring the stack up without Dockman anywhere in the picture.
// That claim had never been executed - it was read, not run.
//
// These tests run the generated script for real, with sops and docker replaced
// by stubs that record what they were asked to do. What stays unverified is the
// part that needs a Linux host: mounting the tmpfs, and the real SOPS and Docker
// binaries. Section 6 of the validation book still has to be run on a VM. What
// no longer needs a VM is the script's own logic, which is where the mistakes
// have actually been.
func recoveryHarness(t *testing.T, composeFile string, requiresRuntimeFiles bool, secretNames ...string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the recovery script targets a POSIX host")
	}

	stack := t.TempDir()
	binDir := t.TempDir()
	journal := filepath.Join(t.TempDir(), "journal")

	// A SOPS-encrypted YAML keeps its top-level keys in clear, which is what the
	// script reads to learn the secret names. Values are ciphertext.
	var source strings.Builder
	for _, name := range secretNames {
		source.WriteString(name + ": ENC[AES256_GCM,data:xxxx,type:str]\n")
	}
	source.WriteString("sops:\n    age:\n        - recipient: age1test\n")
	require.NoError(t, os.WriteFile(filepath.Join(stack, SOPSSourceFile), []byte(source.String()), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(stack, composeFile), []byte("services: {}\n"), 0o600))

	script := filepath.Join(stack, SOPSRecoveryScriptFile)
	require.NoError(t, os.WriteFile(script, []byte(recoveryScript(composeFile, requiresRuntimeFiles)), 0o700))

	// sops stub: serves --extract from the key name, and runs the command for
	// exec-env the way the real one does.
	sopsStub := `#!/bin/sh
echo "sops $*" >> "` + journal + `"
if [ "$1" = "-d" ] && [ "$2" = "--extract" ]; then
  name=$(printf '%s' "$3" | sed 's/^\["//; s/"\]$//')
  printf 'value-of-%s' "$name"
  exit 0
fi
if [ "$1" = "exec-env" ]; then
  shift 2
  exec sh -c "$1"
fi
exit 1
`
	dockerStub := `#!/bin/sh
echo "docker $*" >> "` + journal + `"
exit 0
`
	for name, body := range map[string]string{"sops": sopsStub, "docker": dockerStub} {
		require.NoError(t, os.WriteFile(filepath.Join(binDir, name), []byte(body), 0o755))
	}
	return stack, binDir + string(os.PathListSeparator) + os.Getenv("PATH") + "\x00" + journal
}

func runRecovery(t *testing.T, stack, pathAndJournal string, args ...string) (string, string) {
	t.Helper()
	parts := strings.SplitN(pathAndJournal, "\x00", 2)
	// Each run is judged on what it did, not on what a previous run left behind.
	require.NoError(t, os.WriteFile(parts[1], nil, 0o600))
	command := exec.Command("sh", append([]string{filepath.Join(stack, SOPSRecoveryScriptFile)}, args...)...)
	command.Dir = stack
	command.Env = append(os.Environ(),
		"PATH="+parts[0],
		"SOPS_AGE_KEY_FILE="+filepath.Join(stack, "age-key.txt"),
	)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "the script failed:\n%s", output)
	journal, readErr := os.ReadFile(parts[1])
	require.NoError(t, readErr)
	return string(output), string(journal)
}

// The whole point: a stack with file secrets comes up with nothing but sops and
// docker on the host. An earlier revision of this script told the reader to
// start a Dockman systemd unit here, which defeated its entire purpose.
func TestRecoveryScriptBringsAStackUpWithoutDockman(t *testing.T) {
	stack, env := recoveryHarness(t, "compose.yml", true, "API_TOKEN", "nas-credentials")

	output, journal := runRecovery(t, stack, env)

	require.NotContains(t, strings.ToLower(journal+output), "systemctl")
	require.NotContains(t, strings.ToLower(journal+output), "dockman-secrets-host")

	// Both secrets were materialized, including the dash-bearing name that can
	// only ever be a file.
	for _, name := range []string{"API_TOKEN", "nas-credentials"} {
		value, err := os.ReadFile(filepath.Join(stack, RuntimeDirectory, name))
		require.NoError(t, err, "%s was not materialized", name)
		require.Equal(t, "value-of-"+name, string(value))
	}

	require.Contains(t, journal, "docker compose -f compose.yml up -d --remove-orphans")
	require.Contains(t, journal, "sops exec-env "+SOPSSourceFile,
		"the environment secrets reach Compose through sops, not through a file on disk")
}

// Without file secrets there is nothing to materialize, so the script must not
// create a .secrets directory at all.
func TestRecoveryScriptSkipsMaterializationWhenNoFileSecretIsUsed(t *testing.T) {
	stack, env := recoveryHarness(t, "compose.yml", false, "API_TOKEN")

	_, journal := runRecovery(t, stack, env)

	require.NoDirExists(t, filepath.Join(stack, RuntimeDirectory))
	require.Contains(t, journal, "docker compose -f compose.yml up -d")
	require.NotContains(t, journal, "--extract")
}

// Running as an ordinary user, the tmpfs cannot be mounted. Refusing to start
// would be worse than starting, so the script says plainly that the values are
// landing on disk and offers the command that removes them.
func TestRecoveryScriptWarnsWhenItCannotKeepSecretsOffDisk(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("this is the non-root path")
	}
	stack, env := recoveryHarness(t, "compose.yml", true, "API_TOKEN")

	output, _ := runRecovery(t, stack, env)

	require.Contains(t, output, "secrets will be written to disk")
	require.Contains(t, output, "secrets-clean")
	require.FileExists(t, filepath.Join(stack, RuntimeDirectory, "API_TOKEN"))
}

// secrets-clean is what that warning points at, so it has to work.
func TestRecoveryScriptCleansUpWhatItWroteToDisk(t *testing.T) {
	stack, env := recoveryHarness(t, "compose.yml", true, "API_TOKEN")
	runRecovery(t, stack, env)
	require.FileExists(t, filepath.Join(stack, RuntimeDirectory, "API_TOKEN"))

	_, journal := runRecovery(t, stack, env, "secrets-clean")

	require.NoDirExists(t, filepath.Join(stack, RuntimeDirectory))
	require.NotContains(t, journal, "compose -f compose.yml up",
		"cleaning up is not a reason to start the stack")
}

// An unknown action must not fall through to Compose with something the user
// did not ask for.
func TestRecoveryScriptRefusesAnUnknownAction(t *testing.T) {
	stack, env := recoveryHarness(t, "compose.yml", true, "API_TOKEN")
	parts := strings.SplitN(env, "\x00", 2)

	command := exec.Command("sh", filepath.Join(stack, SOPSRecoveryScriptFile), "rm-everything")
	command.Dir = stack
	command.Env = append(os.Environ(), "PATH="+parts[0], "SOPS_AGE_KEY_FILE="+filepath.Join(stack, "age-key.txt"))
	output, err := command.CombinedOutput()

	require.Error(t, err)
	require.Contains(t, string(output), "usage:")
	require.NoFileExists(t, parts[1], "nothing may run before the action is recognised")
}

// The identity is the one thing the script cannot supply for itself, so it has
// to stop with a message rather than fail somewhere inside SOPS.
func TestRecoveryScriptStopsWithoutTheAgeIdentity(t *testing.T) {
	stack, env := recoveryHarness(t, "compose.yml", true, "API_TOKEN")
	parts := strings.SplitN(env, "\x00", 2)

	command := exec.Command("sh", filepath.Join(stack, SOPSRecoveryScriptFile))
	command.Dir = stack
	command.Env = append(os.Environ(), "PATH="+parts[0])
	output, err := command.CombinedOutput()

	require.Error(t, err)
	require.Contains(t, string(output), "SOPS_AGE_KEY_FILE")
}

// The script is run from anywhere, so it has to work on its own directory
// rather than on the caller's.
func TestRecoveryScriptWorksFromAnotherDirectory(t *testing.T) {
	stack, env := recoveryHarness(t, "docker-compose.yaml", true, "API_TOKEN")
	parts := strings.SplitN(env, "\x00", 2)

	command := exec.Command("sh", filepath.Join(stack, SOPSRecoveryScriptFile))
	command.Dir = t.TempDir()
	command.Env = append(os.Environ(), "PATH="+parts[0], "SOPS_AGE_KEY_FILE="+filepath.Join(stack, "age-key.txt"))
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))

	require.FileExists(t, filepath.Join(stack, RuntimeDirectory, "API_TOKEN"))
	journal, readErr := os.ReadFile(parts[1])
	require.NoError(t, readErr)
	require.Contains(t, string(journal), "docker compose -f docker-compose.yaml up -d")
}
