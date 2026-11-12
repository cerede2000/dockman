package notifications

import (
	"testing"

	"github.com/RA341/dockman/internal/docker/updater"
	"gorm.io/gorm"
)

// Saving, disabling or deleting a channel forgets what it last notified. A
// channel disabled while an error resolves is skipped by NotifyScan (it only
// walks the enabled destinations), so nothing else would ever reset its
// fingerprints: re-enabled later, the same outage would be judged "already
// notified" for good. Only the erasure at save time protects against that.

const (
	staleErrorFingerprint     = "stale-error-fingerprint"
	staleAvailableFingerprint = "stale-available-fingerprint"
)

// seedState inserts a dedup state with non-empty fingerprints for one key, on
// two schedules, as a notifier that already announced something would leave it.
func seedState(t *testing.T, db *gorm.DB, host, key string) {
	t.Helper()
	for _, schedule := range []string{scanSchedule, "0 3 * * *"} {
		row := ChannelNotificationState{
			Host: host, Schedule: schedule, ChannelKey: key,
			LastErrorFingerprint: staleErrorFingerprint, LastAvailableFingerprint: staleAvailableFingerprint,
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func stateRows(t *testing.T, db *gorm.DB, host, key string) []ChannelNotificationState {
	t.Helper()
	var rows []ChannelNotificationState
	if err := db.Where("host = ? AND channel_key = ?", host, key).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// expectState fails unless the key has its two seeded rows with their
// fingerprints untouched.
func expectState(t *testing.T, db *gorm.DB, host, key, what string) {
	t.Helper()
	rows := stateRows(t, db, host, key)
	if len(rows) != 2 {
		t.Fatalf("%s: %d dedup row(s) for host %q key %q, want 2 (it must be left alone)", what, len(rows), host, key)
	}
	for _, row := range rows {
		if row.LastErrorFingerprint != staleErrorFingerprint || row.LastAvailableFingerprint != staleAvailableFingerprint {
			t.Fatalf("%s: fingerprints of host %q key %q were altered: %#v", what, host, key, row)
		}
	}
}

func expectNoState(t *testing.T, db *gorm.DB, host, key, what string) {
	t.Helper()
	if rows := stateRows(t, db, host, key); len(rows) != 0 {
		t.Fatalf("%s: %d dedup row(s) with fingerprints (e.g. error %q) survive for host %q key %q; they must be erased or a resolved outage stays \"already notified\" after re-enabling", what, len(rows), rows[0].LastErrorFingerprint, host, key)
	}
}

func smtpChannelInput(id uint, name string, enabled bool) ChannelInput {
	return ChannelInput{
		ID: id, Name: name, Type: ChannelSMTP, Enabled: enabled, Server: "smtp.example.com", Port: 587,
		Security: SecuritySTARTTLS, FromAddress: "Dockman <dockman@example.com>", Recipients: "ops@example.com",
		NotifyUpdates: true, NotifyErrors: true,
	}
}

func TestSavingTheSMTPConfigurationErasesItsDedupState(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabling"
		if enabled {
			name = "re-enabling"
		}
		t.Run(name, func(t *testing.T) {
			service, db, _ := freshTestService(t)
			seedState(t, db, "local", "smtp")
			expectState(t, db, "local", "smtp", "precondition")
			if err := db.Create(&NotificationState{Host: "local", Schedule: scanSchedule, LastErrorFingerprint: staleErrorFingerprint}).Error; err != nil {
				t.Fatal(err)
			}
			input := validInput()
			input.Enabled = enabled
			if _, err := service.Save("local", input); err != nil {
				t.Fatal(err)
			}
			expectNoState(t, db, "local", "smtp", "Save with Enabled="+name)
			var legacy int64
			if err := db.Model(&NotificationState{}).Where("host = ?", "local").Count(&legacy).Error; err != nil {
				t.Fatal(err)
			}
			if legacy != 0 {
				t.Fatalf("Save with Enabled=%v left %d legacy NotificationState row(s); they must be erased too", enabled, legacy)
			}
		})
	}
}

func TestSavingAChannelErasesItsDedupState(t *testing.T) {
	cases := []struct {
		name          string
		before, after bool
		label         string
	}{
		{"disabling", true, false, "alerts"},
		{"re-enabling", false, true, "alerts"},
		{"renaming a channel that stays enabled", true, true, "alerts-renamed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, db, _ := freshTestService(t)
			view, err := service.SaveChannel("local", smtpChannelInput(0, "alerts", tc.before))
			if err != nil {
				t.Fatal(err)
			}
			key := channelStateKey(view.ID)
			seedState(t, db, "local", key)
			expectState(t, db, "local", key, "precondition")
			if _, err := service.SaveChannel("local", smtpChannelInput(view.ID, tc.label, tc.after)); err != nil {
				t.Fatal(err)
			}
			expectNoState(t, db, "local", key, "SaveChannel when "+tc.name)
		})
	}
}

func TestDeletingAChannelErasesItsDedupState(t *testing.T) {
	service, db, _ := freshTestService(t)
	view, err := service.SaveChannel("local", smtpChannelInput(0, "alerts", true))
	if err != nil {
		t.Fatal(err)
	}
	key := channelStateKey(view.ID)
	seedState(t, db, "local", key)
	expectState(t, db, "local", key, "precondition")
	if err := service.DeleteChannel("local", view.ID); err != nil {
		t.Fatal(err)
	}
	expectNoState(t, db, "local", key, "DeleteChannel")
}

// The erasure is for the channel that was saved, on its own host: another
// channel's memory, or the same channel's on another host, keeps its
// fingerprints, or that one would announce an outage a second time.
func TestErasingDedupStateLeavesOtherChannelsAndHostsAlone(t *testing.T) {
	setup := func(t *testing.T) (*Service, *gorm.DB, uint, uint) {
		service, db, _ := freshTestService(t)
		target, err := service.SaveChannel("local", smtpChannelInput(0, "target", true))
		if err != nil {
			t.Fatal(err)
		}
		other, err := service.SaveChannel("local", smtpChannelInput(0, "other", true))
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{channelStateKey(target.ID), channelStateKey(other.ID), "smtp"} {
			seedState(t, db, "local", key)
		}
		// Same keys on another host.
		for _, key := range []string{channelStateKey(target.ID), "smtp"} {
			seedState(t, db, "remote", key)
		}
		return service, db, target.ID, other.ID
	}
	check := func(t *testing.T, db *gorm.DB, targetID, otherID uint, erased string) {
		t.Helper()
		expectNoState(t, db, "local", erased, "the saved or deleted key")
		if erased != channelStateKey(targetID) {
			expectState(t, db, "local", channelStateKey(targetID), "the other key of the same host")
		}
		if erased != "smtp" {
			expectState(t, db, "local", "smtp", "the SMTP key of the same host")
		}
		expectState(t, db, "local", channelStateKey(otherID), "another channel of the same host")
		expectState(t, db, "remote", channelStateKey(targetID), "the same channel key on another host")
		expectState(t, db, "remote", "smtp", "the SMTP key on another host")
	}
	t.Run("SaveChannel", func(t *testing.T) {
		service, db, target, other := setup(t)
		if _, err := service.SaveChannel("local", smtpChannelInput(target, "target", false)); err != nil {
			t.Fatal(err)
		}
		check(t, db, target, other, channelStateKey(target))
	})
	t.Run("DeleteChannel", func(t *testing.T) {
		service, db, target, other := setup(t)
		if err := service.DeleteChannel("local", target); err != nil {
			t.Fatal(err)
		}
		check(t, db, target, other, channelStateKey(target))
	})
	t.Run("Save", func(t *testing.T) {
		service, db, target, other := setup(t)
		input := validInput()
		input.Enabled = false
		if _, err := service.Save("local", input); err != nil {
			t.Fatal(err)
		}
		check(t, db, target, other, "smtp")
	})
}

// The whole motif: an outage notified, the channel disabled while it resolves
// (no scan reaches the channel to reset it), re-enabled, and the same outage
// comes back. It must be announced again, and a disabled channel must never
// have received anything.
func TestAChannelDisabledWhileAnOutageResolvesAnnouncesItAgainWhenReenabled(t *testing.T) {
	service, _, sender := freshTestService(t)
	view, err := service.SaveChannel("local", smtpChannelInput(0, "alerts", true))
	if err != nil {
		t.Fatal(err)
	}
	run := scheduledRun(scanSchedule)
	failing := registryFailure("error-pages", updater.RegistryErrorOther, "manifest unknown")

	expectMessages(t, "first scan with a confirmed error", scan(t, service, sender, run, failing), 1)

	if _, err := service.SaveChannel("local", smtpChannelInput(view.ID, "alerts", false)); err != nil {
		t.Fatal(err)
	}
	expectMessages(t, "scan with no error while the channel is disabled", scan(t, service, sender, run, registryCurrent("error-pages")), 1)
	expectMessages(t, "scan with the error while the channel is disabled", scan(t, service, sender, run, failing), 1)
	expectMessages(t, "scan with no error while the channel is disabled, again", scan(t, service, sender, run, registryCurrent("error-pages")), 1)

	if _, err := service.SaveChannel("local", smtpChannelInput(view.ID, "alerts", true)); err != nil {
		t.Fatal(err)
	}
	expectMessages(t, "the same error coming back after re-enabling", scan(t, service, sender, run, failing), 2)
}
