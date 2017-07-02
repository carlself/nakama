package server

import (
	"errors"
	"sync"

	"github.com/gogo/protobuf/proto"
	"github.com/satori/go.uuid"
	"go.uber.org/zap"
	"database/sql"
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
	PLAYER_JOIN  opcode = 0
	MOVE         opcode = 1
	HIT          opcode = 2
)

type teamType int32

const (
	Hider teamType = 0
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
	life 			int32
	objectId  int32
	//send 			chan []byte
}

func (mp *matchPlayer) marshal()([]byte, error)  {
	state := &PlayerState{UserId:mp.user.Id,Name:mp.user.Fullname,
		Position:&Vector{mp.position.x,mp.position.y,mp.position.z}, Rotation:mp.rotation, Team:int32(mp.team),
		ObjectId:mp.objectId, Life:mp.life,
	}
	return state.Marshal()
}

type match struct {
	sync.Mutex
	logger  *zap.Logger
	db			*sql.DB
	registry *SessionRegistry
	forward chan[]proto.Message
	join chan Presence
	leave chan Presence
	users   map[Presence]*matchPlayer
	hiderCount int32
	seekerCount int32
	matchId uuid.UUID
}

func NewMatch(l *zap.Logger, db *sql.DB, registry *SessionRegistry, matchId uuid.UUID) *match {
	logger := l.With(zap.String("match", matchId.String()))

	m := &match{
		logger:  	logger,
		db:				db,
		registry: registry,
		join: 		make(chan Presence),
		leave:		make(chan Presence),
		users:   	make(map[Presence]*matchPlayer),
		matchId: 	matchId,
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

	//receivers := make([]Presence, len(m.users))
	//for k := range m.users {
	//	receivers = append(receivers, k)
	//}

	m.users[ps] = mu

	data, err := mu.marshal()
	if err != nil {
		return errors.New("Marshal user state error")
	}

	//state := &PlayerState{UserId:ps.UserID.Bytes(),Name:mu.user.Fullname,
	//	Position:&Vector{0.0,0.0,0.0}, Rotation:0.0, Team:int32(mu.team),
	//	ObjectId:mu.objectId, Life:mu.life,
	//}
	//data, _ := state.Marshal()
	println("notify new match user data to others", ps.UserID.String())
	err = m.broadcastNotification(PLAYER_JOIN, []*MatchUserData{&MatchUserData{UserId:ps.UserID.Bytes(), Data:data}}, func(p Presence)bool {
		return p == ps
	})
	if err != nil {
		return errors.New("Broadcast user data error")
	}
	//m.sendNotification(receivers, PLAYER_JOIN, []*MatchUserData{&MatchUserData{UserId:ps.UserID.Bytes(), Data:data}}, nil)
	if len(m.users) > 1 {
		otherUserData, err := m.aggregatePlayerState(nil)
		if err != nil {
			return errors.New("Marshal other users data error")
		}
		err = m.sendNotification(ps, PLAYER_JOIN, otherUserData)

		if err != nil {
			return errors.New("Notify other user data error")
		}
	}

	return nil
}

func (m *match) initPlayer(mu *matchPlayer) {
	mu.team = teamType(len(m.users) % 2)
	if mu.team == Hider {
		m.hiderCount ++;
		mu.objectId = ObjectId + m.hiderCount;
	} else {
		m.seekerCount++;
		mu.life = 5
	}
}

func (m *match) playerLeave(user Presence) {
	if m.users[user].team == Hider {
		m.hiderCount--;
	} else if m.users[user].team == Seeker {
		m.seekerCount--;
	}
	delete(m.users, user)
}

func (m *match) assignTeam(mu *matchPlayer) {
	mu.team = teamType(len(m.users) % 2)
}

func (m *match) aggregatePlayerState(userFilter func(Presence)bool) ([]*MatchUserData, error){
	var s []*MatchUserData
	for ps, mu := range m.users {
		if userFilter != nil && userFilter(ps){
			continue
		}

		state := &PlayerState{UserId:ps.UserID.Bytes(),Name:mu.user.Fullname,
			Position:&Vector{mu.position.x,mu.position.y,mu.position.z}, Rotation:mu.rotation, Team:int32(mu.team),
			ObjectId:mu.objectId, Life:mu.life,
		}
		data, err := state.Marshal()

		if err != nil {
			return s, err
		}

		s = append(s, &MatchUserData{UserId:ps.UserID.Bytes(), Data:data})
	}

	return s, nil
}

func (m *match) op(sessionId uuid.UUID, userId uuid.UUID, code int64, data []byte) error {
	mu := m.getMatchPlayer(sessionId, userId)
	if mu == nil {
		return errors.New("Cound not retieve user in match")
	}
	switch opcode(code) {
	case MOVE:
		m.playerMove(mu, data)
	case HIT:
		m.playerHit(mu, data)
	}

	return nil
}

func (m *match) playerMove(mu *matchPlayer, data []byte) {
	state := &PlayerState{}
	err := proto.Unmarshal(data, state)
	println("move", state.GoString())
	if err == nil {
		if state.Position != nil {
			mu.position = vector{state.Position.X, state.Position.Y, state.Position.Z}
		}
		mu.rotation = state.Rotation
	}
}

func (m *match) playerHit(mu *matchPlayer, data []byte) {

}

func (m *match) getMatchPlayer(sessionId uuid.UUID, userId uuid.UUID) *matchPlayer {
	m.Lock()
	defer func() {m.Unlock()}()

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

func (m *match) broadcastNotification(op opcode, data []*MatchUserData, userFilter func(Presence)bool) error  {
	outgoing := &Envelope{Payload:&Envelope_MatchNotify{
		MatchNotify:&MatchNotify{
			OpCode:int64(op),
			Data:data,
		}}}

	payload, err := proto.Marshal(outgoing)
	if err != nil {
		m.logger.Error("Could not marshall message to byte[]", zap.Error(err))
		return err
	}

	for p  := range m.users{
		if userFilter(p) {
			continue
		}
		session := m.registry.Get(p.ID.SessionID)
		if session != nil {
			session.SendBytes(payload)
		} else {
			m.logger.Warn("No session to route to", zap.Any("p", p))
			return  err
		}
	}

	return nil
	//m.messageRouter.Send(m.logger, receiver, outgoing)
}

func (m *match)sendNotification(ps Presence, op opcode, data []*MatchUserData) error {
	outgoing := &Envelope{Payload:&Envelope_MatchNotify{
		MatchNotify:&MatchNotify{
			OpCode:int64(op),
			Data:data,
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

