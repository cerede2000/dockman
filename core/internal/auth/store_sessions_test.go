package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func sessionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Discard},
	)
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Session{}))
	return db
}

func countSessions(t *testing.T, db *gorm.DB, unscoped bool) int64 {
	t.Helper()
	var count int64
	query := db.Model(&Session{})
	if unscoped {
		query = query.Unscoped()
	}
	require.NoError(t, query.Count(&count).Error)
	return count
}

// CleanupExpiredSessions existed but nothing ever called it, so a Dockman that
// had been up for a while carried every session it had ever issued. Logging in
// is the natural moment to sweep: it costs nothing when nobody logs in, which
// a scheduled sweep cannot claim.
func TestNewSessionRemovesExpiredSessions(t *testing.T) {
	db := sessionTestDB(t)
	store := NewSessionGormDB(db, 10)

	user := User{Username: "alice", EncryptedPassword: "x"}
	require.NoError(t, db.Create(&user).Error)

	expired := Session{UserID: user.ID, HashedToken: "stale", Expires: time.Now().Add(-time.Hour)}
	require.NoError(t, db.Create(&expired).Error)
	live := Session{UserID: user.ID, HashedToken: "live", Expires: time.Now().Add(time.Hour)}
	require.NoError(t, db.Create(&live).Error)

	fresh := Session{UserID: user.ID, HashedToken: "fresh", Expires: time.Now().Add(time.Hour)}
	require.NoError(t, store.NewSession(&fresh))

	require.Equal(t, int64(2), countSessions(t, db, true),
		"the expired session must actually leave the table, not linger soft-deleted")
	_, err := store.GetSessionByToken("stale")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = store.GetSessionByToken("live")
	require.NoError(t, err, "a session that is still valid is never touched")
}

// An expired session used to count towards the per-user cap, so a user who left
// old sessions behind had their live ones evicted in their place.
func TestNewSessionCapCountsOnlyLiveSessions(t *testing.T) {
	db := sessionTestDB(t)
	store := NewSessionGormDB(db, 2)

	user := User{Username: "bob", EncryptedPassword: "x"}
	require.NoError(t, db.Create(&user).Error)
	for _, token := range []string{"expired-1", "expired-2"} {
		require.NoError(t, db.Create(&Session{
			UserID: user.ID, HashedToken: token, Expires: time.Now().Add(-time.Hour),
		}).Error)
	}
	keep := Session{UserID: user.ID, HashedToken: "keep", Expires: time.Now().Add(time.Hour)}
	require.NoError(t, store.NewSession(&keep))

	next := Session{UserID: user.ID, HashedToken: "next", Expires: time.Now().Add(time.Hour)}
	require.NoError(t, store.NewSession(&next))

	for _, token := range []string{"keep", "next"} {
		_, err := store.GetSessionByToken(token)
		require.NoError(t, err, "%s was evicted by sessions that had already expired", token)
	}
	require.Equal(t, int64(2), countSessions(t, db, true))
}

// The cap itself has to hard-delete too, otherwise every session ever issued
// stays in the table under a deleted_at stamp.
func TestNewSessionCapRemovesSurplusForGood(t *testing.T) {
	db := sessionTestDB(t)
	store := NewSessionGormDB(db, 1)

	user := User{Username: "carol", EncryptedPassword: "x"}
	require.NoError(t, db.Create(&user).Error)
	for _, token := range []string{"first", "second", "third"} {
		require.NoError(t, store.NewSession(&Session{
			UserID: user.ID, HashedToken: token, Expires: time.Now().Add(time.Hour),
		}))
	}

	require.Equal(t, int64(1), countSessions(t, db, true))
	_, err := store.GetSessionByToken("third")
	require.NoError(t, err)
}

// Sweeping is global: one user logging in clears what other users left behind,
// which is what keeps the table bounded on a host with several accounts.
func TestCleanupExpiredSessionsRemovesRowsForEveryUser(t *testing.T) {
	db := sessionTestDB(t)
	store := NewSessionGormDB(db, 10)

	for _, name := range []string{"dave", "erin"} {
		user := User{Username: name, EncryptedPassword: "x"}
		require.NoError(t, db.Create(&user).Error)
		require.NoError(t, db.Create(&Session{
			UserID: user.ID, HashedToken: name + "-old", Expires: time.Now().Add(-time.Minute),
		}).Error)
	}

	require.NoError(t, store.CleanupExpiredSessions())
	require.Equal(t, int64(0), countSessions(t, db, true))
}
