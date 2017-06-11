package server

import (
	"errors"
	"sync"

	"github.com/gogo/protobuf/proto"
	"github.com/satori/go.uuid"
	"go.uber.org/zap"
)

// 广播游戏事件
// 运行游戏逻辑
// 管理游戏玩家
// 服务器自主运行的逻辑， 客户端触发的逻辑

type opcode int64

const (
	PLAYER_JOIN  opcode = 0
	MOVE         opcode = 1
	HIT          opcode = 2
)

type teamType int32

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
}

type match struct {
	sync.Mutex
	logger  *zap.Logger
	forward chan[]proto.Message
	join chan Presence
	leave chan Presence
	users   map[Presence]*matchPlayer
	matchId uuid.UUID
	p       *pipeline
}

func NewMatch(l *zap.Logger, p *pipeline, matchId uuid.UUID) *match {
	logger := l.With(zap.String("match", matchId.String()))

	m := &match{
		logger:  	logger,
		join: 		make(chan Presence),
		leave:		make(chan Presence),
		users:   	make(map[Presence]*matchPlayer),
		matchId: 	matchId,
		p:       	p,
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
	users, err := UsersFetchIds(m.logger, m.p.db, [][]byte{ps.UserID.Bytes()})
	if err != nil {
		return err
	}

	if len(users) == 0 {
		return errors.New("cound not retrieve user in match")
	}

	mu := &matchPlayer{
		user: users[0],
	}
	m.assignTeam(mu)

	receivers := make([]Presence, len(m.users))
	for k := range m.users {
		receivers = append(receivers, k)
	}

	m.users[ps] = mu

	state := &PlayerState{UserId:ps.UserID.Bytes(), Position:&Vector{0.0,0.0,0.0}, Rotation:0.0, Team:int32(mu.team)}
	data, _ := state.Marshal()
	println("notify new match user data to others", ps.UserID.String())
	m.sendNotify(receivers, PLAYER_JOIN, []*MatchUserData{&MatchUserData{UserId:ps.UserID.Bytes(), Data:data}})
	if len(m.users) != 0 {
		println("")
		m.sendNotify([]Presence{ps}, PLAYER_JOIN, m.aggregatePlayersData())
	}

	return nil
}

func (m *match) playerLeave(user Presence) {
	//m.Lock()
	delete(m.users, user)
	//m.Unlock()
}

func (m *match) assignTeam(mu *matchPlayer) {
	mu.team = teamType(len(m.users) % 2)
}

func (m *match) aggregatePlayersData() []*MatchUserData{
	var s []*MatchUserData
	for ps, mu := range m.users {
		state := &PlayerState{UserId:ps.UserID.Bytes(), Position:&Vector{mu.position.x,mu.position.y,mu.position.z}, Rotation:mu.rotation, Team:int32(mu.team)}
		data, _ := state.Marshal()
		s = append(s, &MatchUserData{UserId:ps.UserID.Bytes(), Data:data})
	}

	return s
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

func (m *match) sendMatchData(sender Presence, receiver []Presence, op opcode, msg proto.Message) {
	outgoing := &Envelope{
		Payload: &Envelope_MatchData{
			MatchData: &MatchData{
				MatchId: m.matchId.Bytes(),
				Presence: &UserPresence{
					UserId:    sender.UserID.Bytes(),
					SessionId: sender.ID.SessionID.Bytes(),
					Handle:    sender.Meta.Handle,
				},
				OpCode: int64(op),
				Data:   []byte(msg.String()),
			},
		},
	}
	m.p.messageRouter.Send(m.logger, receiver, outgoing)
}

func (m *match) sendNotify(receiver []Presence, op opcode, data []*MatchUserData)  {
	outgoing := &Envelope{Payload:&Envelope_MatchNotify{
		MatchNotify:&MatchNotify{
			OpCode:int64(op),
			Data:data,
		}}}

	m.p.messageRouter.Send(m.logger, receiver, outgoing)
}
