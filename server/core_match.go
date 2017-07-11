package server

import (
	"errors"
	"sync"

	"database/sql"
	"github.com/gogo/protobuf/proto"
	"github.com/satori/go.uuid"
	"go.uber.org/zap"
)

// 广播游戏事件
// 运行游戏逻辑
// 管理游戏玩家
// 服务器自主运行的逻辑， 客户端触发的逻辑

const (
	ObjectId = 10000
)

type opcode int32

const (
	PLAYER_JOIN opcode = 0
	PLAYER_MOVE opcode = 1
	PLAYER_HIT  opcode = 2
)

type teamType int32

const (
	Hider  teamType = 0
	Seeker teamType = 1
)

type vector struct {
	x float32
	y float32
	z float32
}

type matchPlayer struct {
	user     *User
	team     teamType
	position vector
	rotation float32
	life     int32
	objectId int32
}

func (mp *matchPlayer) marshal() ([]byte, error) {
	state := &PlayerState{UserId: mp.user.Id, Name: mp.user.Fullname,
		Position: &Vector{mp.position.x, mp.position.y, mp.position.z}, Rotation: mp.rotation, Team: int32(mp.team),
		ObjectId: mp.objectId, Life: mp.life,
	}
	return state.Marshal()
}

type match struct {
	sync.RWMutex
	name     string
	logger   *zap.Logger
	db       *sql.DB
	registry *SessionRegistry
	forward  chan []proto.Message
	//join        chan Presence
	//leave       chan Presence
	users       map[Presence]*matchPlayer
	hiderCount  int32
	seekerCount int32
	matchId     uuid.UUID
}

func NewMatch(l *zap.Logger, db *sql.DB, registry *SessionRegistry, matchId uuid.UUID, name string) *match {
	logger := l.With(zap.String("match", matchId.String()))

	m := &match{
		name:     name,
		logger:   logger,
		db:       db,
		registry: registry,
		users:   make(map[Presence]*matchPlayer),
		matchId: matchId,
	}

	//go m.run()

	return m
}


func (m *match) playerJoin(ps Presence) error {
	users, err := UsersFetchIds(m.logger, m.db, [][]byte{ps.UserID.Bytes()})
	if err != nil {
		return err
	}

	if len(users) == 0 {
		return errors.New("Could not retrieve user in match")
	}

	mu := &matchPlayer{
		user: users[0],
	}
	m.initPlayer(mu)
	//println("init player", mu.team, mu.life, mu.objectId)

	m.Lock()
	m.users[ps] = mu
	m.Unlock()

	data, err := mu.marshal()
	s := &PlayerState{}
	s.Unmarshal(data)
	//println(s.String())
	if err != nil {
		return errors.New("Marshal user state error")
	}

	err = m.broadcastNotification(PLAYER_JOIN, []*MatchUserData{&MatchUserData{UserId: ps.UserID.Bytes(), Data: data}}, func(p Presence) bool {
		return p == ps
	})
	if err != nil {
		return errors.New("Broadcast user data error")
	}
	otherUserData, err := m.aggregatePlayerState(nil)
	if err != nil {
		return errors.New("Marshal other users data error")
	}
	err = m.sendNotification(ps, PLAYER_JOIN, otherUserData)

	if err != nil {
		return errors.New("Notify other user data error")
	}

	return nil
}

func (m *match) playerLeave(ps Presence) {
	m.Lock()
	defer m.Unlock()

	mp, ok := m.users[ps]
	if ok {
		if mp.team == Hider {
			m.hiderCount--
		} else if mp.team == Seeker {
			m.seekerCount--
		}
		delete(m.users, ps)
	}
}

func (m *match) playerDisconnect(sessionId uuid.UUID) {
	m.Lock()
	defer m.Unlock()

	for ps, mp := range m.users {
		if ps.ID.SessionID == sessionId {
			if mp.team == Hider {
				m.hiderCount--
			} else if mp.team == Seeker {
				m.seekerCount--
			}
			delete(m.users, ps)
			break
		}
	}
}

func (m *match) isAbandoned() bool {
	m.Lock()
	defer m.Unlock()
	return len(m.users) == 0
}

func (m *match) initPlayer(mu *matchPlayer) {
	mu.team = teamType(len(m.users) % 2)
	if mu.team == Hider {
		m.hiderCount++
		mu.objectId = ObjectId + m.hiderCount
	} else {
		m.seekerCount++
		mu.life = 5
	}
}

func (m *match) assignTeam(mu *matchPlayer) {
	mu.team = teamType(len(m.users) % 2)
}

func (m *match) aggregatePlayerState(userFilter func(Presence) bool) ([]*MatchUserData, error) {
	m.RLock()
	defer m.RUnlock()

	var s []*MatchUserData
	for ps, mu := range m.users {
		if userFilter != nil && userFilter(ps) {
			continue
		}

		state := &PlayerState{UserId: ps.UserID.Bytes(), Name: mu.user.Fullname,
			Position: &Vector{mu.position.x, mu.position.y, mu.position.z}, Rotation: mu.rotation, Team: int32(mu.team),
			ObjectId: mu.objectId, Life: mu.life,
		}
		data, err := state.Marshal()

		if err != nil {
			return s, err
		}

		s = append(s, &MatchUserData{UserId: ps.UserID.Bytes(), Data: data})
	}

	return s, nil
}

func (m *match) op(sessionId uuid.UUID, userId uuid.UUID, meta PresenceMeta, code int64, data []byte) error {
	ps := Presence{
		ID:     PresenceID{Node: m.name, SessionID: sessionId},
		UserID: userId,
		Meta:   meta,
	}

	switch opcode(code) {
	case PLAYER_MOVE:
		return m.playerMove(ps, data)
	case PLAYER_HIT:
		return m.playerHit(ps, data)
	}

	return nil
}

func (m *match) playerMove(ps Presence, data []byte) error {
	m.RLock()
	mu, ok := m.users[ps]
	m.RUnlock()

	if !ok {
		return errors.New("Cound not retieve user in match")
	}

	move := &PlayerMove{}
	err := proto.Unmarshal(data, move)
	//println("move", move.GoString())
	if err == nil {
		if move.Position != nil {
			mu.position = vector{move.Position.X, move.Position.Y, move.Position.Z}
		}
		mu.rotation = move.Rotation
	}

	outgoing := &MatchUserData{
		UserId: ps.UserID.Bytes(),
		Data:   data,
	}
	return m.broadcastNotification(PLAYER_MOVE, []*MatchUserData{outgoing}, filterUser(ps))
}

func (m *match) playerHit(ps Presence, data []byte) error {

	return nil
}

func (m *match) getMatchPlayer(sessionId uuid.UUID, userId uuid.UUID) *matchPlayer {
	m.Lock()
	defer m.Unlock()

	for p, m := range m.users {
		if p.ID.SessionID == sessionId && p.UserID == userId {
			return m
		}
	}

	return nil
}

type filterFunc func(Presence) bool

func filterUser(ps Presence) filterFunc {
	return func(p Presence) bool { return p == ps }
}

func (m *match) broadcastNotification(op opcode, data []*MatchUserData, userFilter func(Presence) bool) error {
	outgoing := &Envelope{Payload: &Envelope_MatchNotify{
		MatchNotify: &MatchNotify{
			OpCode: int64(op),
			Data:   data,
		}}}

	payload, err := proto.Marshal(outgoing)
	if err != nil {
		m.logger.Error("Could not marshall message to byte[]", zap.Error(err))
		return err
	}

	for p := range m.users {
		if userFilter(p) {
			continue
		}
		session := m.registry.Get(p.ID.SessionID)
		if session != nil {
			session.SendBytes(payload)
		} else {
			m.logger.Warn("No session to route to", zap.Any("p", p))
			return err
		}
	}

	return nil
}

func (m *match) sendNotification(ps Presence, op opcode, data []*MatchUserData) error {
	outgoing := &Envelope{Payload: &Envelope_MatchNotify{
		MatchNotify: &MatchNotify{
			OpCode: int64(op),
			Data:   data,
		}}}

	payload, err := proto.Marshal(outgoing)

	if err != nil {
		m.logger.Error("Could not marshall message to byte[]", zap.Error(err))
		return err
	}

	session := m.registry.Get(ps.ID.SessionID)
	if session != nil {
		session.SendBytes(payload)
	} else {
		m.logger.Warn("No session to route to", zap.Any("p", ps))
		return err
	}

	return nil
}
