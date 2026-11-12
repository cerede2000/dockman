package notifications

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/RA341/dockman/internal/docker/updater"
)

const scanSchedule = "*/15 * * * *"

func scheduledRun(schedule string) updater.UpdateScanRun {
	now := time.Now()
	return updater.UpdateScanRun{Host: "local", Trigger: "scheduled", Schedule: schedule, Targets: 1, CompletedAt: &now}
}

func registryFailure(name string, class updater.RegistryErrorClass, reason string) updater.ContainerUpdateCheck {
	return updater.ContainerUpdateCheck{
		ContainerName: name, Image: "ghcr.io/example/" + name + ":latest",
		Status: updater.ContainerUpdateError, Reason: "registry check: " + reason, ErrorClass: class,
	}
}

func registryCurrent(name string) updater.ContainerUpdateCheck {
	return updater.ContainerUpdateCheck{
		ContainerName: name, Image: "ghcr.io/example/" + name + ":latest", Status: updater.ContainerUpdateCurrent,
	}
}

// scan feeds one scheduled scan to the notifier and returns how many messages
// have been sent in total so far.
func scan(t *testing.T, service *Service, sender *recordingSender, run updater.UpdateScanRun, checks ...updater.ContainerUpdateCheck) int {
	t.Helper()
	if err := service.NotifyScan(context.Background(), run, checks); err != nil {
		t.Fatal(err)
	}
	return len(sender.messages)
}

func expectMessages(t *testing.T, step string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("after %s: %d message(s) sent, want %d", step, got, want)
	}
}

const timeout = "Head \"https://ghcr.io/v2/example/error-pages/manifests/latest\": context deadline exceeded"

// The loop that prompted this: a registry that times out once in a while,
// succeeds, then times out again. Each of those used to be a new message.
func TestATransientErrorThatComesAndGoesIsNeverNotified(t *testing.T) {
	service, _, sender := testService(t)
	if _, err := service.Save("local", validInput()); err != nil {
		t.Fatal(err)
	}
	run := scheduledRun(scanSchedule)
	failing := registryFailure("error-pages", updater.RegistryErrorTransient, timeout)

	expectMessages(t, "first timeout", scan(t, service, sender, run, failing), 0)
	expectMessages(t, "success", scan(t, service, sender, run, registryCurrent("error-pages")), 0)
	expectMessages(t, "second timeout", scan(t, service, sender, run, failing), 0)
	expectMessages(t, "second success", scan(t, service, sender, run, registryCurrent("error-pages")), 0)
	expectMessages(t, "third timeout", scan(t, service, sender, run, failing), 0)
}

// Two consecutive scans is a registry that is really in trouble: one message,
// at the second scan, and not another for as long as it lasts.
func TestATransientErrorOnTwoConsecutiveScansIsNotifiedOnce(t *testing.T) {
	service, _, sender := testService(t)
	if _, err := service.Save("local", validInput()); err != nil {
		t.Fatal(err)
	}
	run := scheduledRun(scanSchedule)
	failing := registryFailure("error-pages", updater.RegistryErrorTransient, timeout)

	expectMessages(t, "first timeout", scan(t, service, sender, run, failing), 0)
	expectMessages(t, "second timeout", scan(t, service, sender, run, failing), 1)
	if body := sender.messages[0].Body; !strings.Contains(body, "error-pages") || !strings.Contains(body, "context deadline exceeded") {
		t.Fatalf("the message must still show the raw reason: %s", body)
	}
	expectMessages(t, "third timeout", scan(t, service, sender, run, failing), 1)
	expectMessages(t, "fourth timeout", scan(t, service, sender, run, failing), 1)
}

// An authentication or not-found error is the operator's to fix: no waiting.
func TestAnErrorThatWaitingCannotFixIsNotifiedImmediately(t *testing.T) {
	for _, test := range []struct {
		name  string
		class updater.RegistryErrorClass
	}{
		{"authentication", updater.RegistryErrorAuthentication},
		{"not found", updater.RegistryErrorNotFound},
		{"other", updater.RegistryErrorOther},
		{"unclassified", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, sender := testService(t)
			if _, err := service.Save("local", validInput()); err != nil {
				t.Fatal(err)
			}
			run := scheduledRun(scanSchedule)
			failing := registryFailure("private-app", test.class, "registry authentication required for ghcr.io; add a credential in Settings > Registries")

			expectMessages(t, "first scan", scan(t, service, sender, run, failing), 1)
			expectMessages(t, "second scan", scan(t, service, sender, run, failing), 1)
		})
	}
}

// What the receiver sees as "the same error" is the container, the image and
// the kind of failure. A timeout worded differently by the next scan is not
// news, and neither is an authentication error whose text changed.
func TestTheSameErrorWithAnotherWordingIsNotANewMessage(t *testing.T) {
	t.Run("transient", func(t *testing.T) {
		service, _, sender := testService(t)
		if _, err := service.Save("local", validInput()); err != nil {
			t.Fatal(err)
		}
		run := scheduledRun(scanSchedule)
		expectMessages(t, "first timeout", scan(t, service, sender, run, registryFailure("error-pages", updater.RegistryErrorTransient, timeout)), 0)
		expectMessages(t, "second timeout", scan(t, service, sender, run, registryFailure("error-pages", updater.RegistryErrorTransient, "Get \"https://ghcr.io/token\": net/http: TLS handshake timeout")), 1)
		expectMessages(t, "third, worded again", scan(t, service, sender, run, registryFailure("error-pages", updater.RegistryErrorTransient, "registry manifest request returned HTTP 503")), 1)
	})
	t.Run("authentication", func(t *testing.T) {
		service, _, sender := testService(t)
		if _, err := service.Save("local", validInput()); err != nil {
			t.Fatal(err)
		}
		run := scheduledRun(scanSchedule)
		expectMessages(t, "first refusal", scan(t, service, sender, run, registryFailure("private-app", updater.RegistryErrorAuthentication, "registry authentication required for ghcr.io; add a credential in Settings > Registries")), 1)
		expectMessages(t, "same refusal, worded again", scan(t, service, sender, run, registryFailure("private-app", updater.RegistryErrorAuthentication, "ghcr.io rejected the stored credentials; check them in Settings > Registries")), 1)
	})
	t.Run("a scan that failed as a whole", func(t *testing.T) {
		service, _, sender := testService(t)
		if _, err := service.Save("local", validInput()); err != nil {
			t.Fatal(err)
		}
		run := scheduledRun(scanSchedule)
		run.Error = "list containers: dial tcp 10.0.0.1:2375: i/o timeout"
		expectMessages(t, "first failure", scan(t, service, sender, run), 1)
		run.Error = "list containers: dial tcp 10.0.0.2:2375: connection refused"
		expectMessages(t, "same failure, worded again", scan(t, service, sender, run), 1)
	})
	t.Run("another class is another error", func(t *testing.T) {
		service, _, sender := testService(t)
		if _, err := service.Save("local", validInput()); err != nil {
			t.Fatal(err)
		}
		run := scheduledRun(scanSchedule)
		expectMessages(t, "authentication", scan(t, service, sender, run, registryFailure("app", updater.RegistryErrorAuthentication, "refused")), 1)
		expectMessages(t, "not found", scan(t, service, sender, run, registryFailure("app", updater.RegistryErrorNotFound, "missing")), 2)
	})
}

// A transient error held back must not hide, or ride along with, a permanent
// one in the same scan.
func TestAHeldTransientErrorStaysOutOfTheMessageOfAPermanentOne(t *testing.T) {
	service, _, sender := testService(t)
	if _, err := service.Save("local", validInput()); err != nil {
		t.Fatal(err)
	}
	run := scheduledRun(scanSchedule)
	permanent := registryFailure("private-app", updater.RegistryErrorAuthentication, "registry authentication required for ghcr.io; add a credential in Settings > Registries")
	flaky := registryFailure("error-pages", updater.RegistryErrorTransient, timeout)

	expectMessages(t, "permanent and first timeout", scan(t, service, sender, run, permanent, flaky), 1)
	body := sender.messages[0].Body
	if !strings.Contains(body, "private-app") || strings.Contains(body, "error-pages") {
		t.Fatalf("the held transient error leaked into the message, or the permanent one is missing: %s", body)
	}
	if !strings.Contains(sender.messages[0].Subject, "1 scan error(s)") {
		t.Fatalf("subject counts the held error: %q", sender.messages[0].Subject)
	}
	// The same permanent error is already notified; the timeout is now confirmed.
	expectMessages(t, "second timeout", scan(t, service, sender, run, permanent, flaky), 2)
	if body := sender.messages[1].Body; !strings.Contains(body, "error-pages") {
		t.Fatalf("the confirmed transient error is missing: %s", body)
	}
}

// The count of consecutive scans is per schedule: another cron expression has
// its own history, as it has its own fingerprints.
func TestTheTransientMemoryIsKeptPerSchedule(t *testing.T) {
	service, _, sender := testService(t)
	if _, err := service.Save("local", validInput()); err != nil {
		t.Fatal(err)
	}
	failing := registryFailure("error-pages", updater.RegistryErrorTransient, timeout)
	expectMessages(t, "first schedule", scan(t, service, sender, scheduledRun("*/15 * * * *"), failing), 0)
	expectMessages(t, "other schedule", scan(t, service, sender, scheduledRun("0 4 * * *"), failing), 0)
	expectMessages(t, "first schedule again", scan(t, service, sender, scheduledRun("*/15 * * * *"), failing), 1)
}

// A scan that failed as a whole knows nothing about the targets: it neither
// confirms a transient error nor forgets it.
func TestAScanThatFailedAsAWholeLeavesTheTransientMemoryAlone(t *testing.T) {
	service, _, sender := testService(t)
	if _, err := service.Save("local", validInput()); err != nil {
		t.Fatal(err)
	}
	failing := registryFailure("error-pages", updater.RegistryErrorTransient, timeout)
	expectMessages(t, "first timeout", scan(t, service, sender, scheduledRun(scanSchedule), failing), 0)

	broken := scheduledRun(scanSchedule)
	broken.Error = "list containers: connection refused"
	expectMessages(t, "scan that failed as a whole", scan(t, service, sender, broken), 1)

	expectMessages(t, "timeout again", scan(t, service, sender, scheduledRun(scanSchedule), failing), 2)
}

// An error that disappears during a scan that announces something else must
// stop being remembered as already notified, in both directions. Before, only
// the scan that announced nothing cleared it, so the error that came back
// later was silently taken for the one already sent.
func TestAnErrorThatResolvedDuringAScanWithAnotherAnnouncementCanBeNotifiedAgain(t *testing.T) {
	update := updater.ContainerUpdateCheck{
		ContainerName: "web", Image: "example/web:latest", Status: updater.ContainerUpdateAvailable, RemoteDigest: "sha256:new",
	}
	broken := registryFailure("private-app", updater.RegistryErrorAuthentication, "registry authentication required for ghcr.io; add a credential in Settings > Registries")

	t.Run("error resolved while a new update is announced", func(t *testing.T) {
		service, _, sender := testService(t)
		if _, err := service.Save("local", validInput()); err != nil {
			t.Fatal(err)
		}
		run := scheduledRun(scanSchedule)
		expectMessages(t, "error", scan(t, service, sender, run, broken), 1)
		expectMessages(t, "error gone, update announced", scan(t, service, sender, run, registryCurrent("private-app"), update), 2)
		expectMessages(t, "error back", scan(t, service, sender, run, broken, update), 3)
	})
	t.Run("update resolved while a new error is announced", func(t *testing.T) {
		service, _, sender := testService(t)
		if _, err := service.Save("local", validInput()); err != nil {
			t.Fatal(err)
		}
		run := scheduledRun(scanSchedule)
		expectMessages(t, "update", scan(t, service, sender, run, update), 1)
		expectMessages(t, "update gone, error announced", scan(t, service, sender, run, broken), 2)
		expectMessages(t, "update back", scan(t, service, sender, run, broken, update), 3)
	})
}
