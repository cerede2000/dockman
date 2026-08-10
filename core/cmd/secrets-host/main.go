package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/RA341/dockman/internal/secrets"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: dockman-secrets-host {materialize|cleanup|install}")
	}
	switch os.Args[1] {
	case "materialize":
		set := flag.NewFlagSet("materialize", flag.ExitOnError)
		configPath := set.String("config", secrets.HostRuntimeConfigPath, "host runtime configuration")
		_ = set.Parse(os.Args[2:])
		config, err := secrets.LoadHostRuntimeConfig(*configPath)
		if err != nil {
			fail(err.Error())
		}
		result, err := secrets.MaterializeHostRuntime(context.Background(), config)
		if err != nil {
			fail(err.Error())
		}
		fmt.Printf("materialized %d secret(s) for %d stack(s) into tmpfs\n", result.Secrets, result.Stacks)
	case "cleanup":
		set := flag.NewFlagSet("cleanup", flag.ExitOnError)
		configPath := set.String("config", secrets.HostRuntimeConfigPath, "host runtime configuration")
		_ = set.Parse(os.Args[2:])
		config, err := secrets.LoadHostRuntimeConfig(*configPath)
		if err != nil {
			fail(err.Error())
		}
		if err = secrets.CleanupHostRuntime(config); err != nil {
			fail(err.Error())
		}
		fmt.Println("volatile secret mounts removed")
	case "install":
		set := flag.NewFlagSet("install", flag.ExitOnError)
		stackRoot := set.String("stack-root", "", "absolute host Compose stack root")
		ageKey := set.String("age-key", "", "absolute host age identity path")
		sopsSource := set.String("sops-source", "", "SOPS binary copied from the Dockman image")
		tmpfsSize := set.Int("tmpfs-size-mib", 16, "maximum volatile storage per encrypted stack")
		fileMode := set.Uint("file-mode", 0444, "runtime secret mode: 0400, 0440 or 0444")
		activate := set.Bool("activate", true, "enable and start the systemd service")
		// Repeatable. Dockman writes its reconciliation request through the
		// filesystem of the alias a stack belongs to, and an alias rooted below
		// the stack root cannot write above itself - so each alias root needs
		// its own watch or a nested stack waits forever.
		var watchRoots stringList
		set.Var(&watchRoots, "watch-root", "extra absolute directory to watch for reconciliation requests (repeatable)")
		_ = set.Parse(os.Args[2:])
		executable, err := os.Executable()
		if err != nil {
			fail(err.Error())
		}
		err = secrets.InstallHostRuntime(secrets.HostInstallOptions{
			Config:     secrets.HostRuntimeConfig{StackRoot: *stackRoot, AgeKeyFile: *ageKey, TmpfsSizeMiB: *tmpfsSize, FileMode: uint32(*fileMode)},
			HelperFrom: executable, SOPSFrom: *sopsSource, SystemRoot: "/", Activate: *activate,
			WatchRoots: watchRoots,
		})
		if err != nil {
			fail(err.Error())
		}
		fmt.Println("host secret runtime installed; new encrypted stacks are reconciled automatically; recreate Dockman once so pre-existing tmpfs submounts are visible inside its stack bind mount")
	default:
		fail("unknown action: " + os.Args[1])
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "dockman-secrets-host:", message)
	os.Exit(1)
}

// stringList collects a repeatable flag.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	*l = append(*l, value)
	return nil
}
