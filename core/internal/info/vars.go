package info

import (
	"runtime"
)

// build args to modify vars
//
// -X github.com/RA341/dockman/internal/info.Version=${VERSION} \
// -X github.com/RA341/dockman/internal/info.CommitInfo=${COMMIT_INFO} \
// -X github.com/RA341/dockman/internal/info.BuildDate=$(date -u +'%Y-%m-%dT%H:%M:%SZ') \
// -X github.com/RA341/dockman/internal/info.Branch=${BRANCH}" \
// cmd/server.go

// defaults
const (
	VersionDev = "canary"
	Unknown    = "unknown"
)

type FlavourType string

// flavours
const (
	FlavourDevelop FlavourType = "develop"
	FlavourServer  FlavourType = "server"
	FlavourDocker  FlavourType = "docker"
	FlavourDesktop FlavourType = "desktop"
)

// defaultFlavour is what a binary that never called app.InitMeta runs as.
// It has to be the strictest one: the development flavour turns on
// relaxations - an OIDC client that skips certificate verification, chiefly -
// that must be opted into explicitly and never inherited by omission.
const defaultFlavour = FlavourServer

var (
	Flavour    = defaultFlavour
	Version    = VersionDev
	CommitInfo = Unknown
	BuildDate  = Unknown
	Branch     = Unknown
	GoVersion  = runtime.Version()
)

func SetFlavour(f FlavourType) {
	Flavour = f
}

func IsKnown(val string) bool {
	return val != Unknown
}

func IsDocker() bool {
	return Flavour == FlavourDocker
}

func IsDev() bool {
	return Flavour == FlavourDevelop
}

func IsDesktop() bool {
	return Flavour == FlavourDesktop
}
