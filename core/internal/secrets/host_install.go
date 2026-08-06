package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type HostInstallOptions struct {
	Config     HostRuntimeConfig
	HelperFrom string
	SOPSFrom   string
	SystemRoot string
	Activate   bool
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
	socketDropInTarget := rooted(root, "/etc/systemd/system/docker.socket.d/20-dockman-secrets.conf")
	for _, destination := range []string{helperTarget, sopsTarget, configTarget, unitTarget, reconcileUnitTarget, reconcilePathTarget, dropInTarget, socketDropInTarget} {
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
	unit := `[Unit]
Description=Materialize encrypted Compose secrets into volatile memory
Documentation=https://github.com/cerede2000/dockman
After=local-fs.target
Before=docker.service docker.socket

[Service]
Type=oneshot
ExecStart=/usr/local/libexec/dockman-secrets-host materialize --config /etc/dockman-secrets-host.json
ExecStop=/usr/local/libexec/dockman-secrets-host cleanup --config /etc/dockman-secrets-host.json
RemainAfterExit=yes
NoNewPrivileges=yes

[Install]
WantedBy=multi-user.target
`
	dropIn := `[Unit]
Requires=dockman-secrets-host.service
After=dockman-secrets-host.service
`
	reconcileUnit := `[Unit]
Description=Reconcile encrypted Compose secrets into volatile memory
Documentation=https://github.com/cerede2000/dockman
Requires=dockman-secrets-host.service
After=local-fs.target dockman-secrets-host.service
StartLimitIntervalSec=10
StartLimitBurst=5

[Service]
Type=oneshot
ExecStart=/usr/local/libexec/dockman-secrets-host materialize --config /etc/dockman-secrets-host.json
NoNewPrivileges=yes
`
	reconcilePath := fmt.Sprintf(`[Unit]
Description=Watch for explicit Dockman secret reconciliation requests
Documentation=https://github.com/cerede2000/dockman
After=local-fs.target

[Path]
PathChanged=%q
Unit=%s

[Install]
WantedBy=multi-user.target
`, filepath.Join(options.Config.StackRoot, HostRuntimeReconcileRequestFile), HostReconcileUnitName)
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
	if err = writeHostFileAtomic(socketDropInTarget, []byte(dropIn), 0o644); err != nil {
		return fmt.Errorf("write Docker socket systemd dependency: %w", err)
	}
	if options.Activate && root == "/" {
		for _, args := range [][]string{{"daemon-reload"}, {"enable", HostRuntimeUnitName}, {"enable", "--now", HostReconcilePathName}, {"restart", HostRuntimeUnitName}} {
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
