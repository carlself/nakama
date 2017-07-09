package server

import (
	"database/sql"
	"errors"
	"github.com/satori/go.uuid"
	"go.uber.org/zap"
)

type MatchTracker interface {
	Join(matchID uuid.UUID, allowEmpty bool, sessionID uuid.UUID, userID uuid.UUID, meta PresenceMeta) error
	Leave(matchID uuid.UUID, sessionID uuid.UUID, userID uuid.UUID, meta PresenceMeta) error
	FindMatch(matchId uuid.UUID) (*match, bool)
}

type matchTrackerService struct {
	name     string
	logger   *zap.Logger
	db       *sql.DB
	registry *SessionRegistry
	values   map[uuid.UUID]*match
}

func NewMatchTrackerService(logger *zap.Logger, db *sql.DB, registry *SessionRegistry, name string) MatchTracker {
	return &matchTrackerService{
		name:     name,
		logger:   logger,
		db:       db,
		registry: registry,
		values:   make(map[uuid.UUID]*match),
	}
}

func (s *matchTrackerService) Join(matchID uuid.UUID, allowEmpty bool, sessionID uuid.UUID, userID uuid.UUID, meta PresenceMeta) error {
	m, ok := s.values[matchID]

	if ok {
		m.join <- Presence{
			ID:     PresenceID{Node: s.name, SessionID: sessionID},
			UserID: userID,
			Meta:   meta}
	} else if allowEmpty {
		m = NewMatch(s.logger, s.db, s.registry, matchID, s.name)
		s.values[matchID] = m

		m.join <- Presence{
			ID:     PresenceID{Node: s.name, SessionID: sessionID},
			UserID: userID,
			Meta:   meta}
	} else {
		return errors.New("Match not found")
	}

	return nil
}

func (s *matchTrackerService) Leave(matchID uuid.UUID, sessionID uuid.UUID, userID uuid.UUID, meta PresenceMeta) error {
	m, ok := s.values[matchID]

	if ok {
		m.leave <- Presence{
			ID:     PresenceID{Node: s.name, SessionID: sessionID},
			UserID: userID,
			Meta:   meta}
	} else {
		return errors.New("Match not found")
	}

	return nil
}

func (s *matchTrackerService) FindMatch(matchId uuid.UUID) (*match, bool) {
	m, ok := s.values[matchId]
	return m, ok
}
