package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type HostInstallOptions struct {
	Config     HostRuntimeConfig
	HelperFrom string
	SOPSFrom   string
	SystemRoot string
	Activate   bool
	// WatchRoots are extra directories in which Dockman may drop a
	// reconciliation request. Dockman writes that request through the
	// filesystem of the alias the stack belongs to, and an alias rooted below
	// the stack root cannot write above itself - so a request for a nested
	// alias lands somewhere nothing was watching, and the user waits on a
	// reconciliation that never comes. The stack root is always watched; these
	// are the alias roots on top of it.
	WatchRoots []string
}

func InstallHostRuntime(options HostInstallOptions) error {
	root := filepath.Clean(options.SystemRoot)
	if root == "." {
		root = "/"
	}
	if !filepath.IsAbs(root) {
		return errors.New("system root must be absolute")
	}
	if root == "/" && os.Geteuid() != 0 {
		return errors.New("host runtime installation must run as root")
	}
	options.Config.SOPSBinary = HostRuntimeSOPSPath
	if err := options.Config.normalize(); err != nil {
		return err
	}
	helperTarget := rooted(root, HostRuntimeBinaryPath)
	sopsTarget := rooted(root, HostRuntimeSOPSPath)
	configTarget := rooted(root, HostRuntimeConfigPath)
	unitTarget := rooted(root, "/etc/systemd/system/"+HostRuntimeUnitName)
	reconcileUnitTarget := rooted(root, "/etc/systemd/system/"+HostReconcileUnitName)
	reconcilePathTarget := rooted(root, "/etc/systemd/system/"+HostReconcilePathName)
	dropInTarget := rooted(root, "/etc/systemd/system/docker.service.d/20-dockman-secrets.conf")
	// Installed by earlier revisions. Ordering this unit before docker.socket
	// closed a systemd cycle (docker.socket -> sockets.target -> basic.target ->
	// this service -> docker.socket), and systemd broke it by dropping the
	// Docker start job entirely: the host booted without a Docker daemon.
	staleSocketDropInTarget := rooted(root, "/etc/systemd/system/docker.socket.d/20-dockman-secrets.conf")
	for _, destination := range []string{helperTarget, sopsTarget, configTarget, unitTarget, reconcileUnitTarget, reconcilePathTarget, dropInTarget} {
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
	}
	if err := copyRegularExecutable(options.HelperFrom, helperTarget); err != nil {
		return fmt.Errorf("install host helper: %w", err)
	}
	if err := copyRegularExecutable(options.SOPSFrom, sopsTarget); err != nil {
		return fmt.Errorf("install SOPS: %w", err)
	}
	encoded, err := json.MarshalIndent(options.Config, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err = writeHostFileAtomic(configTarget, encoded, 0o600); err != nil {
		return fmt.Errorf("write host runtime configuration: %w", err)
	}
	// ExecStop stays: stopping this unit deliberately must take the plaintext
	// out of memory, and dropping it would leave every stack's tmpfs mounted
	// until the next reboot. The hazard was never the cleanup itself, it was
	// that installation activated the unit with `restart` - which runs ExecStop
	// first and therefore unmounted the secrets of running containers on every
	// reinstall. The activation below uses the reconcile unit instead.
	unit := `[Unit]
Description=Materialize encrypted Compose secrets into volatile memory
Documentation=https://github.com/cerede2000/dockman
After=local-fs.target
Before=docker.service

[Service]
Type=oneshot
ExecStart=/usr/local/libexec/dockman-secrets-host materialize --config /etc/dockman-secrets-host.json
ExecStop=/usr/local/libexec/dockman-secrets-host cleanup --config /etc/dockman-secrets-host.json
RemainAfterExit=yes
NoNewPrivileges=yes

[Install]
WantedBy=multi-user.target
`
	// Wants, not Requires: a failed secret materialization must not keep the
	// whole Docker daemon down. After= still holds, so dockerd only starts once
	// this unit has finished activating — successfully or not. When it succeeds
	// every container therefore sees its secrets; when it fails the host stays
	// administrable and only the stacks that need secrets are affected.
	dropIn := `[Unit]
Wants=dockman-secrets-host.service
After=dockman-secrets-host.service
`
	// StartLimitIntervalSec=10 with StartLimitBurst=5 was reachable by simply
	// encrypting six stacks in a row: the unit entered a failed state and took
	// the .path watch down with it, permanently. Dockman now coalesces its
	// requests to one per operation, and the remaining allowance is wide enough
	// that a legitimate burst cannot exhaust it.
	reconcileUnit := `[Unit]
Description=Reconcile encrypted Compose secrets into volatile memory
Documentation=https://github.com/cerede2000/dockman
Requires=dockman-secrets-host.service
After=local-fs.target dockman-secrets-host.service
StartLimitIntervalSec=60
StartLimitBurst=30

[Service]
Type=oneshot
ExecStart=/usr/local/libexec/dockman-secrets-host materialize --config /etc/dockman-secrets-host.json
NoNewPrivileges=yes
`
	// The rate limit belongs on the watch, where systemd throttles triggers
	// instead of failing the unit outright: a burst is delayed, never fatal.
	reconcilePath := fmt.Sprintf(`[Unit]
Description=Watch for explicit Dockman secret reconciliation requests
Documentation=https://github.com/cerede2000/dockman
After=local-fs.target

[Path]
%s
Unit=%s
TriggerLimitIntervalSec=60
TriggerLimitBurst=30

[Install]
WantedBy=multi-user.target
`, reconcileWatchDirectives(options), HostReconcileUnitName)
	if err = writeHostFileAtomic(unitTarget, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}
	if err = writeHostFileAtomic(reconcileUnitTarget, []byte(reconcileUnit), 0o644); err != nil {
		return fmt.Errorf("write systemd reconcile unit: %w", err)
	}
	if err = writeHostFileAtomic(reconcilePathTarget, []byte(reconcilePath), 0o644); err != nil {
		return fmt.Errorf("write systemd reconcile path: %w", err)
	}
	if err = writeHostFileAtomic(dropInTarget, []byte(dropIn), 0o644); err != nil {
		return fmt.Errorf("write Docker systemd dependency: %w", err)
	}
	// Hosts provisioned by an earlier revision still carry the cycle-inducing
	// socket drop-in. Removing it here is what actually repairs them, since the
	// unit file alone no longer references docker.socket.
	if err = os.Remove(staleSocketDropInTarget); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove obsolete Docker socket systemd dependency: %w", err)
	}
	if options.Activate && root == "/" {
		// Never `restart` the main unit here. RemainAfterExit=yes makes restart
		// run ExecStop first, which unmounts every stack's tmpfs - so each
		// reinstall pulled the secrets out from under the containers that were
		// running. `start` brings it up the first time and is a no-op after,
		// and the reconcile unit re-materializes without ever tearing down.
		for _, args := range [][]string{{"daemon-reload"}, {"enable", HostRuntimeUnitName}, {"enable", "--now", HostReconcilePathName}, {"start", HostRuntimeUnitName}, {"start", HostReconcileUnitName}} {
			if output, runErr := exec.Command("systemctl", args...).CombinedOutput(); runErr != nil {
				return fmt.Errorf("systemctl %v: %s", args, string(output))
			}
		}
	}
	return nil
}

func rooted(root, absolute string) string {
	return filepath.Join(root, filepath.Clean(absolute))
}

func copyRegularExecutable(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 256<<20 {
		return errors.New("source is not a regular file")
	}
	value, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return writeHostFileAtomic(destination, value, 0o755)
}

func writeHostFileAtomic(destination string, value []byte, mode os.FileMode) error {
	directory := filepath.Dir(destination)
	temporary, err := os.CreateTemp(directory, ".dockman-install-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(mode); err == nil {
		_, err = temporary.Write(value)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, destination)
}

// reconcileWatchDirectives lists every request file the reconcile watch must
// react to, one PathChanged= per line.
//
// The stack root always comes first: it is the directory the daemon walks, and
// on a single-alias host it is the only one that matters. Alias roots follow,
// because Dockman writes its request through the alias filesystem of the stack
// concerned, which cannot reach above its own root. Watching a request file
// that never appears costs nothing; not watching one costs a reconciliation
// that never happens.
//
// An alias created after installation is not watched until the host runtime is
// installed again - the units are written once, from what was known then.
func reconcileWatchDirectives(options HostInstallOptions) string {
	roots := append([]string{options.Config.StackRoot}, options.WatchRoots...)
	seen := make(map[string]struct{}, len(roots))
	lines := make([]string, 0, len(roots))
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		// A relative or empty root would make systemd reject the whole unit,
		// taking the stack root down with it.
		if !filepath.IsAbs(root) || root == string(filepath.Separator) || strings.ContainsAny(root, "\r\n") {
			continue
		}
		if _, duplicate := seen[root]; duplicate {
			continue
		}
		seen[root] = struct{}{}
		// Bare, not %q. systemd does not unquote this setting: it reads the
		// leading double quote as the first character of the path, decides the
		// path is not absolute, drops the directive - and then refuses the
		// whole unit for having no path at all. The watch has been dead since
		// it was first written this way, so nothing ever reconciled
		// automatically; only an explicit `systemctl start
		// dockman-secrets-host.service` did. A path with a space is fine
		// unquoted here, since systemd takes the rest of the line.
		lines = append(lines, "PathChanged="+filepath.Join(root, HostRuntimeReconcileRequestFile))
	}
	return strings.Join(lines, "\n")
}
