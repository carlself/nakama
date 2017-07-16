package server

import (
	"database/sql"
	"errors"
	"github.com/satori/go.uuid"
	"go.uber.org/zap"
	"sync"
)

type MatchTracker interface {
	Join(matchID uuid.UUID, allowEmpty bool, sessionID uuid.UUID, userID uuid.UUID, meta PresenceMeta) error
	Leave(matchID uuid.UUID, sessionID uuid.UUID, userID uuid.UUID, meta PresenceMeta) error
	FindMatch(matchId uuid.UUID) (*match, bool)
	RemoveAll(sessionId uuid.UUID)
}

type matchTrackerService struct {
	sync.RWMutex
	name            string
	logger          *zap.Logger
	db              *sql.DB
	registry        *SessionRegistry
	values          map[uuid.UUID]*match
	sessionMatchMap map[uuid.UUID]uuid.UUID
}

func NewMatchTrackerService(logger *zap.Logger, db *sql.DB, registry *SessionRegistry, name string) MatchTracker {
	return &matchTrackerService{
		name:            name,
		logger:          logger,
		db:              db,
		registry:        registry,
		values:          make(map[uuid.UUID]*match),
		sessionMatchMap: make(map[uuid.UUID]uuid.UUID),
	}
}

func (s *matchTrackerService) Join(matchID uuid.UUID, allowEmpty bool, sessionID uuid.UUID, userID uuid.UUID, meta PresenceMeta) error {
	s.Lock()
	defer  s.Unlock()
	m, ok := s.values[matchID]

	if ok {
		m.playerJoin(Presence{
			ID:     PresenceID{Node: s.name, SessionID: sessionID},
			UserID: userID,
			Meta:   meta})

		s.sessionMatchMap[sessionID] = matchID
	} else if allowEmpty {
		m = NewMatch(s.logger, s.db, s.registry, matchID, s.name)
		s.logger.Info("Match created", zap.String("MatchId", matchID.String()))

		s.values[matchID] = m

		m.playerJoin(Presence{
			ID:     PresenceID{Node: s.name, SessionID: sessionID},
			UserID: userID,
			Meta:   meta})

		s.sessionMatchMap[sessionID] = matchID
	} else {
		return errors.New("Match not found")
	}

	return nil
}

func (s *matchTrackerService) Leave(matchID uuid.UUID, sessionID uuid.UUID, userID uuid.UUID, meta PresenceMeta) error {
	s.Lock()
	defer  s.Unlock();
	m, ok := s.values[matchID]

	if ok {
		m.playerLeave(Presence{
			ID:     PresenceID{Node: s.name, SessionID: sessionID},
			UserID: userID,
			Meta:   meta})

		delete(s.sessionMatchMap, sessionID)

		if m.isAbandoned() {
			delete (s.values, matchID)
			s.logger.Info("Match abandoned", zap.String("MatchId", matchID.String()))
		}
	} else {
		return errors.New("Match not found")
	}

	return nil
}

func (s *matchTrackerService) FindMatch(matchId uuid.UUID) (*match, bool) {
	s.RLock()
	defer s.RUnlock()
	m, ok := s.values[matchId]
	return m, ok
}

func (s *matchTrackerService) RemoveAll(sessionID uuid.UUID) {
	s.Lock()
	defer s.Unlock()

	matchId, ok := s.sessionMatchMap[sessionID]
	if ok {
		delete(s.sessionMatchMap, sessionID)
		match, ok := s.values[matchId]

		if ok {
			match.playerDisconnect(sessionID)
			if match.isAbandoned() {
				delete (s.values, matchId)
				s.logger.Info("Match abandoned", zap.String("MatchId", matchId.String()))
			}
		}
	}
}
