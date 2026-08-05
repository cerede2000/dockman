package container

import (
	"context"
	"errors"
	"testing"
)

func TestShortContainerIDHandlesTransientEmptyValues(t *testing.T) {
	if got := shortContainerID(""); got != "" {
		t.Fatalf("empty id = %q", got)
	}
	if got := shortContainerID("0123456789abcdef"); got != "0123456789ab" {
		t.Fatalf("short id = %q", got)
	}
}

func TestTransientStatsErrorsIncludeContainerReplacementWindow(t *testing.T) {
	for _, err := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		errors.New("Error response from daemon: No such container: old"),
		errors.New("Error response from daemon: invalid id: id is empty"),
	} {
		if !transientStatsError(err) {
			t.Fatalf("replacement-window error was not transient: %v", err)
		}
	}
	if transientStatsError(errors.New("permission denied")) {
		t.Fatal("actionable stats error was hidden as transient")
	}
}
