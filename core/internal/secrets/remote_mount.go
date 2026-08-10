package secrets

import (
	"errors"
	"strings"
)

// RemoteRuntimeMountCommand builds the shell command that reports what, if
// anything, is mounted on a stack's runtime directory of a remote host.
//
// findmnt --target answers with the enclosing mount when the path is not itself
// a mount point, so the target it prints has to be compared with the path we
// asked about - which is why the command reports TARGET rather than testing a
// string on the remote side. That comparison, and the verdict that follows from
// it, belong here next to the local one they have to agree with.
func RemoteRuntimeMountCommand(absolutePath string) string {
	quoted := "'" + strings.ReplaceAll(absolutePath, "'", "'\"'\"'") + "'"
	return "findmnt -rn -o TARGET,FSTYPE,SOURCE --target " + quoted + " 2>/dev/null"
}

// findmnt -r escapes whitespace in the target as \x20 and friends. Only the
// target can contain any, so the two trailing fields are always clean.
var findmntUnescaper = strings.NewReplacer(
	`\x20`, " ", `\x09`, "\t", `\x0a`, "\n", `\x5c`, `\`,
)

// ClassifyRemoteRuntimeMount reads that command's output and reaches the same
// three verdicts as pathIsMounted does locally: ours, nothing, or somebody
// else's.
//
// The remote check used to compare FSTYPE and SOURCE against a fixed string and
// return "not mounted" for anything else. A foreign filesystem sitting on
// .secrets therefore read as an empty directory on an SSH host, and Dockman
// went on to write plaintext into a mount it does not own - the exact case the
// local check refuses by name.
func ClassifyRemoteRuntimeMount(absolutePath, output string) (bool, error) {
	return classifyRuntimeMount(absolutePath, output)
}

func classifyRuntimeMount(absolutePath, output string) (bool, error) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimRight(line, "\r"))
		if len(fields) < 3 {
			continue
		}
		fstype, source := fields[len(fields)-2], fields[len(fields)-1]
		target := findmntUnescaper.Replace(strings.Join(fields[:len(fields)-2], " "))
		if target != absolutePath {
			// The enclosing mount: this path is not a mount point of its own.
			continue
		}
		if fstype == "tmpfs" && source == RuntimeMountSource {
			return true, nil
		}
		return false, errors.New("runtime secret directory is occupied by an unmanaged mount")
	}
	return false, nil
}
