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
	//send 			chan []byte
}

func (mp *matchPlayer) marshal() ([]byte, error) {
	state := &PlayerState{UserId: mp.user.Id, Name: mp.user.Fullname,
		Position: &Vector{mp.position.x, mp.position.y, mp.position.z}, Rotation: mp.rotation, Team: int32(mp.team),
		ObjectId: mp.objectId, Life: mp.life,
	}
	return state.Marshal()
}

type match struct {
	sync.Mutex
	name        string
	logger      *zap.Logger
	db          *sql.DB
	registry    *SessionRegistry
	forward     chan []proto.Message
	join        chan Presence
	leave       chan Presence
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
		join:     make(chan Presence),
		leave:    make(chan Presence),
		users:    make(map[Presence]*matchPlayer),
		matchId:  matchId,
	}

	go m.run()

	return m
}

func (m *match) run() {
	for {
		select {
		case ps := <-m.join:
			m.playerJoin(ps)
		case ps := <-m.leave:
			m.playerLeave(ps)
		case msg := <-m.forward:
			println(msg)
		}
	}
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
	println("init player", mu.team, mu.life, mu.objectId)

	m.users[ps] = mu

	data, err := mu.marshal()
	s := &PlayerState{}
	s.Unmarshal(data)
	println(s.String())
	if err != nil {
		return errors.New("Marshal user state error")
	}

	println("notify new match user data to others", ps.UserID.String())
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

func (m *match) playerLeave(user Presence) {
	if m.users[user].team == Hider {
		m.hiderCount--
	} else if m.users[user].team == Seeker {
		m.seekerCount--
	}
	delete(m.users, user)
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
	mu, ok := m.users[ps]
	if !ok {
		return errors.New("Cound not retieve user in match")
	}

	move := &PlayerMove{}
	err := proto.Unmarshal(data, move)
	println("move", move.GoString())
	if err == nil {
		if move.Position != nil {
			mu.position = vector{move.Position.X, move.Position.Y, move.Position.Z}
		}
		mu.rotation = move.Rotation
	}

	outgoing := &MatchUserData{
		UserId:ps.UserID.Bytes(),
		Data:data,
	}
	return m.broadcastNotification(PLAYER_MOVE, []*MatchUserData{outgoing},filterUser(ps))
}

func (m *match) playerHit(ps Presence, data []byte) error {

	return nil
}

func (m *match) getMatchPlayer(sessionId uuid.UUID, userId uuid.UUID) *matchPlayer {
	m.Lock()
	defer func() { m.Unlock() }()

	for p, m := range m.users {
		if p.ID.SessionID == sessionId && p.UserID == userId {
			return m
		}
	}

	return nil
}

//func (m *match) sendMatchData(sender Presence, receiver []Presence, op opcode, msg proto.Message) {
//	outgoing := &Envelope{
//		Payload: &Envelope_MatchData{
//			MatchData: &MatchData{
//				MatchId: m.matchId.Bytes(),
//				Presence: &UserPresence{
//					UserId:    sender.UserID.Bytes(),
//					SessionId: sender.ID.SessionID.Bytes(),
//					Handle:    sender.Meta.Handle,
//				},
//				OpCode: int64(op),
//				Data:   []byte(msg.String()),
//			},
//		},
//	}
//	m.messageRouter.Send(m.logger, receiver, outgoing)
//}

type filterFunc func(Presence) bool

func filterUser(ps Presence) filterFunc  {
	return func(p Presence)bool { return p==ps}
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
