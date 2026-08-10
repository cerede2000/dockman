package auth

import (
	"time"

	"gorm.io/gorm"
)

type SessionGormDB struct {
	db                 *gorm.DB
	maxSessionsPerUser uint
}

func NewSessionGormDB(db *gorm.DB, maxSessions uint) SessionStore {
	return &SessionGormDB{db: db, maxSessionsPerUser: maxSessions}
}

func (s *SessionGormDB) NewSession(session *Session) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Logging in is the moment to sweep. A scheduled sweep would burn a
		// timer on a host where nobody ever logs in, and no sweep at all - which
		// is what CleanupExpiredSessions amounted to, since nothing called it -
		// left every session Dockman had ever issued in the table.
		//
		// It also has to happen before the count below: an expired session that
		// still counts towards the per-user cap evicts a live one in its place.
		if err := deleteExpiredSessions(tx); err != nil {
			return err
		}

		err := tx.Create(&session).Error
		if err != nil {
			return err
		}

		var count int64
		err = tx.Model(&Session{}).
			Where("user_id = ?", session.UserID).
			Count(&count).Error
		if err != nil {
			return err
		}

		maxSessions := int64(s.maxSessionsPerUser)
		if count > maxSessions {
			sessionsToDelete := count - maxSessions

			var oldSessions []Session
			// Find the oldest session IDs to delete
			if err := tx.Where("user_id = ?", session.UserID).
				Order("created_at ASC").
				Limit(int(sessionsToDelete)).
				Find(&oldSessions).Error; err != nil {
				return err
			}

			for _, oldSession := range oldSessions {
				// Unscoped: a session evicted by the cap is gone for good.
				// Soft-deleting it hides it from every query that matters while
				// the row stays behind forever.
				if err := tx.Unscoped().Delete(&oldSession).Error; err != nil {
					return err
				}
			}
		}

		return nil
	})
}

// deleteExpiredSessions drops every session past its expiry, for all users. A
// token that expired is already refused at verification, so keeping the row
// buys nothing and costs a table that only ever grows.
func deleteExpiredSessions(tx *gorm.DB) error {
	return tx.Unscoped().Where("expires < ?", time.Now()).Delete(&Session{}).Error
}

func (s *SessionGormDB) DeleteSession(sessionID uint) error {
	result := s.db.Unscoped().Delete(&Session{}, sessionID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *SessionGormDB) GetSession(sessionID uint) (Session, error) {
	var session Session
	err := s.db.First(&session, sessionID).Error
	return session, err
}

func (s *SessionGormDB) GetSessionByToken(token string) (Session, error) {
	var session Session
	err := s.db.
		Preload("User").
		Where("hashed_token = ?", token).
		First(&session).
		Error
	return session, err
}

func (s *SessionGormDB) CleanupExpiredSessions() error {
	return deleteExpiredSessions(s.db)
}
