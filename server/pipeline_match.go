// Copyright 2017 The Nakama Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"fmt"
	"unicode/utf8"

	"github.com/dgrijalva/jwt-go"
	"github.com/satori/go.uuid"
	"go.uber.org/zap"
)

type matchDataFilter struct {
	userID    uuid.UUID
	sessionID uuid.UUID
}

func (p *pipeline) matchCreate(logger *zap.Logger, session *session, envelope *Envelope) {
	matchID := uuid.NewV4()

	handle := session.handle.Load()

	p.tracker.Track(session.id, "match:"+matchID.String(), session.userID, PresenceMeta{
		Handle: handle,
	})

	self := &UserPresence{
		UserId:    session.userID.Bytes(),
		SessionId: session.id.Bytes(),
		Handle:    handle,
	}

	session.Send(&Envelope{CollationId: envelope.CollationId, Payload: &Envelope_Match{Match: &TMatch{Match: &Match{
		MatchId:   matchID.Bytes(),
		Presences: []*UserPresence{self},
		Self:      self,
	}}}})
}

func (p *pipeline) matchJoin(logger *zap.Logger, session *session, envelope *Envelope) {
	e := envelope.GetMatchesJoin()

	if len(e.Matches) == 0 {
		session.Send(ErrorMessageBadInput(envelope.CollationId, "At least one item must be present"))
		return
	} else if len(e.Matches) > 1 {
		logger.Warn("There are more than one item passed to the request - only processing the first item.")
	}

	m := e.Matches[0]

	var matchID uuid.UUID
	var err error
	allowEmpty := false

	switch m.Id.(type) {
	case *TMatchesJoin_MatchJoin_MatchId:
		matchID, err = uuid.FromBytes(m.GetMatchId())
		if err != nil {
			session.Send(ErrorMessageBadInput(envelope.CollationId, "Invalid match ID"))
			return
		}
	case *TMatchesJoin_MatchJoin_Token:
		tokenBytes := m.GetToken()
		if controlCharsRegex.Match(tokenBytes) {
			session.Send(ErrorMessageBadInput(envelope.CollationId, "Match token cannot contain control chars"))
			return
		}
		if !utf8.Valid(tokenBytes) {
			session.Send(ErrorMessageBadInput(envelope.CollationId, "Match token must only contain valid UTF-8 bytes"))
			return
		}
		token, err := jwt.Parse(string(tokenBytes), func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("Unexpected signing method: %v", token.Header["alg"])
			}
			return p.hmacSecretByte, nil
		})
		if err != nil {
			session.Send(ErrorMessageBadInput(envelope.CollationId, "Match token is invalid"))
			return
		}
		if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
			matchID, err = uuid.FromString(claims["mid"].(string))
			if err != nil {
				session.Send(ErrorMessageBadInput(envelope.CollationId, "Match token is invalid"))
				return
			}
		} else {
			session.Send(ErrorMessageBadInput(envelope.CollationId, "Match token is invalid"))
			return
		}
		allowEmpty = true
	case nil:
		session.Send(ErrorMessageBadInput(envelope.CollationId, "No match ID or token found"))
		return
	default:
		session.Send(ErrorMessageBadInput(envelope.CollationId, "Unrecognized match ID or token"))
		return
	}

	handle := session.handle.Load()
	err = p.matchTracker.Join(matchID, allowEmpty, session.id, session.userID, PresenceMeta{Handle: handle})

	if err != nil {
		session.Send(ErrorMessage(envelope.CollationId, MATCH_NOT_FOUND, err.Error()))
		return
	}

	session.Send(&Envelope{CollationId: envelope.CollationId, Payload: &Envelope_Matches{Matches: &TMatches{
		Matches: []*Match{
			&Match{
				MatchId:   matchID.Bytes(),
			},
		},
	}}})
}

func (p *pipeline) matchLeave(logger *zap.Logger, session *session, envelope *Envelope) {
	e := envelope.GetMatchesLeave()

	if len(e.MatchIds) == 0 {
		session.Send(ErrorMessageBadInput(envelope.CollationId, "At least one item must be present"))
		return
	} else if len(e.MatchIds) > 1 {
		logger.Warn("There are more than one item passed to the request - only processing the first item.")
	}

	m := e.MatchIds[0]
	matchID, err := uuid.FromBytes(m)
	if err != nil {
		session.Send(ErrorMessageBadInput(envelope.CollationId, "Invalid match ID"))
		return
	}

	handle := session.handle.Load()
	err = p.matchTracker.Leave(matchID, session.id, session.userID, PresenceMeta{Handle: handle})
	if err != nil {
		session.Send(ErrorMessage(envelope.CollationId, MATCH_NOT_FOUND, err.Error()))
		return
	}

	session.Send(&Envelope{CollationId: envelope.CollationId})
}

func (p *pipeline) matchDataSend(logger *zap.Logger, session *session, envelope *Envelope) {
	incoming := envelope.GetMatchDataSend()
	matchIDBytes := incoming.MatchId
	matchID, err := uuid.FromBytes(matchIDBytes)
	if err != nil {
		return
	}

	m, ok := p.matchTracker.FindMatch(matchID)
	if ok {
		err := m.op(session.id, session.userID, PresenceMeta{Handle:session.handle.Load()},incoming.OpCode, incoming.Data)
		if err != nil {
			println(err)
		}
	} else {
		return
	}
}
