package server

import (
	"github.com/satori/go.uuid"
	"go.uber.org/zap"
	"errors"
	"database/sql"
)

type MatchTracker interface {
	Join(matchID uuid.UUID, allowEmpty bool, sessionID uuid.UUID, userID uuid.UUID, meta PresenceMeta) error
}

type matchTrackerService struct {
	name string
	logger *zap.Logger
	db *sql.DB
	registry *SessionRegistry
	values map[uuid.UUID]*match
}

func NewMatchTrackerService(logger *zap.Logger,db *sql.DB,registry *SessionRegistry, name string) MatchTracker  {
	return &matchTrackerService{
		name:			name,
		logger: 	logger,
		db:				db,
		registry: registry,
		values:		make(map[uuid.UUID]*match),
	}
}

func (s *matchTrackerService) Join(matchID uuid.UUID, allowEmpty bool, sessionID uuid.UUID, userID uuid.UUID, meta PresenceMeta) error  {
	m, ok := s.values[matchID]

	if ok {
		m.join<-Presence{
			ID:			PresenceID{Node:s.name, SessionID:sessionID },
			UserID:		userID,
			Meta: 		meta}
	} else if allowEmpty {
		m = NewMatch(s.logger, s.db, s.registry, matchID)
		s.values[matchID] = m

		m.join<-Presence{
			ID:			PresenceID{Node:s.name, SessionID:sessionID},
			UserID:		userID,
			Meta: 		meta}
	} else {
		return errors.New("Match not found")
	}

	return nil
}
