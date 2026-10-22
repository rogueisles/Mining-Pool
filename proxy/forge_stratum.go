package proxy

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/HawyUkr/open-progpow-pool/util"
)

func parseForgeWorkerName(user string) (string, string, error) {
	user = strings.TrimSpace(user)
	parts := strings.SplitN(user, ".", 2)
	login := strings.ToLower(parts[0])
	if !util.IsValidHexAddress(login) {
		return "", "", errors.New("invalid OGG address")
	}
	worker := "0"
	if len(parts) == 2 && parts[1] != "" {
		if !workerPattern.MatchString(parts[1]) {
			return "", "", errors.New("invalid worker name")
		}
		worker = parts[1]
	}
	return login, worker, nil
}

func (cs *Session) handleForgeTCPMessage(s *ProxyServer, req *StratumReq) error {
	switch req.Method {
	case "mining.configure":
		// Phase 5C does not negotiate version rolling yet. Returning an empty
		// result map is the standard way to decline optional extensions.
		return cs.sendStratumResult(req.Id, map[string]interface{}{})

	case "mining.subscribe":
		cs.forgeStateMu.Lock()
		if len(cs.extraNonce1) == 0 {
			cs.extraNonce1 = s.forge.nextExtraNonce1()
		}
		cs.subscribed = true
		cs.forgeStateMu.Unlock()
		subID := hex.EncodeToString(cs.extraNonce1)
		subscriptions := []interface{}{
			[]interface{}{"mining.set_difficulty", subID},
			[]interface{}{"mining.notify", subID},
		}
		result := []interface{}{subscriptions, subID, s.forge.extraNonce2Size}
		if err := cs.sendStratumResult(req.Id, result); err != nil {
			return err
		}
		if cs.authorized {
			if job := s.forge.currentJob(); job != nil {
				if err := cs.pushForgeWork(s, job); err != nil {
					return err
				}
			}
			s.registerSession(cs)
		}
		return nil

	case "mining.extranonce.subscribe":
		return cs.sendStratumResult(req.Id, true)

	case "mining.authorize":
		var params []string
		if err := json.Unmarshal(req.Params, &params); err != nil || len(params) < 1 {
			return cs.sendStratumError(req.Id, &ErrorReply{Code: 20, Message: "Invalid authorize params"})
		}
		login, worker, err := parseForgeWorkerName(params[0])
		if err != nil {
			return cs.sendStratumError(req.Id, &ErrorReply{Code: 24, Message: err.Error()})
		}
		if s.forge.account.workPaused() {
			return cs.sendStratumError(req.Id, &ErrorReply{Code: 20, Message: "Accounting capacity unavailable; retry later"})
		}
		if s.forgeVarDiffEnabled() && len(params) > 1 {
			requested, found, err := forgePasswordDifficulty(params[1])
			if err != nil {
				return cs.sendStratumError(req.Id, &ErrorReply{Code: 20, Message: err.Error()})
			}
			if found {
				cs.requestForgeDifficulty(s, requested, time.Now())
			}
		}
		if !s.policy.ApplyLoginPolicy(login, cs.ip) {
			return cs.sendStratumError(req.Id, &ErrorReply{Code: 24, Message: "You are blacklisted"})
		}
		cs.forgeStateMu.Lock()
		cs.login = login
		cs.worker = worker
		cs.authorized = true
		cs.forgeStateMu.Unlock()
		if err := cs.sendStratumResult(req.Id, true); err != nil {
			return err
		}
		log.Printf("FORGE Stratum miner authorized %v.%v@%v", login, worker, cs.ip)
		if cs.subscribed {
			if job := s.forge.currentJob(); job != nil {
				if err := cs.pushForgeWork(s, job); err != nil {
					return err
				}
			}
			s.registerSession(cs)
		}
		return nil

	case "mining.suggest_difficulty":
		if !s.forgeVarDiffEnabled() {
			return cs.sendStratumResult(req.Id, false)
		}
		requested, err := decodeForgeSuggestedDifficulty(req.Params)
		if err != nil {
			return cs.sendStratumError(req.Id, &ErrorReply{Code: 20, Message: err.Error()})
		}
		cs.requestForgeDifficulty(s, requested, time.Now())
		return cs.sendStratumResult(req.Id, true)
	case "mining.submit":
		if !cs.subscribed {
			return cs.sendStratumError(req.Id, &ErrorReply{Code: 25, Message: "Not subscribed"})
		}
		if !cs.authorized {
			return cs.sendStratumError(req.Id, &ErrorReply{Code: 24, Message: "Unauthorized worker"})
		}
		var params []string
		if err := json.Unmarshal(req.Params, &params); err != nil || len(params) < 5 {
			s.policy.ApplyMalformedPolicy(cs.ip)
			return cs.sendStratumError(req.Id, &ErrorReply{Code: 20, Message: "Invalid submit params"})
		}
		jobID, extra2, ntime, nonce := params[1], strings.ToLower(params[2]), strings.ToLower(params[3]), strings.ToLower(params[4])
		valid, duplicate, _, err := s.processForgeShare(cs, cs.login, cs.worker, jobID, extra2, ntime, nonce, time.Now())
		if errors.Is(err, errForgeStale) && s.forge.account.workPaused() {
			// Work withheld by the pool can expire at an otherwise honest miner.
			return cs.sendStratumError(req.Id, &ErrorReply{Code: 20, Message: "Share accounting temporarily unavailable"})
		}
		// Pool outages and admission pressure are not miner misconduct. Do not
		// feed them into invalid-share banning or a Redis-backed policy call.
		if err != nil && !duplicate && !errors.Is(err, errForgeDuplicate) && !errors.Is(err, errForgeStale) && !errors.Is(err, errForgeLowDifficulty) && !errors.Is(err, errForgeMalformed) {
			return cs.sendStratumError(req.Id, &ErrorReply{Code: 20, Message: "Share accounting temporarily unavailable"})
		}
		shareOK := valid && !duplicate
		policyOK := s.policy.ApplySharePolicy(cs.ip, shareOK)

		if duplicate || errors.Is(err, errForgeDuplicate) {
			return cs.sendStratumError(req.Id, &ErrorReply{Code: 22, Message: "Duplicate share"})
		}
		if errors.Is(err, errForgeStale) {
			return cs.sendStratumError(req.Id, &ErrorReply{Code: 21, Message: "Stale share"})
		}
		if errors.Is(err, errForgeLowDifficulty) {
			if !policyOK {
				return cs.sendStratumError(req.Id, &ErrorReply{Code: 23, Message: "Low difficulty share"})
			}
			return cs.sendStratumError(req.Id, &ErrorReply{Code: 23, Message: "Low difficulty share"})
		}
		if err != nil {
			if errors.Is(err, errForgeMalformed) {
				s.policy.ApplyMalformedPolicy(cs.ip)
				return cs.sendStratumError(req.Id, &ErrorReply{Code: 20, Message: "Malformed share"})
			}
			log.Printf("FORGE share processing error from %s@%s: %v", cs.login, cs.ip, err)
			return cs.sendStratumError(req.Id, &ErrorReply{Code: 20, Message: "Share processing unavailable"})
		}
		if !valid {
			return cs.sendStratumError(req.Id, &ErrorReply{Code: 23, Message: "Invalid share"})
		}
		if !policyOK {
			return cs.sendStratumError(req.Id, &ErrorReply{Code: 20, Message: "High rate of invalid shares"})
		}
		cs.recordForgeAccepted(s, jobID)
		return cs.sendStratumResult(req.Id, true)

	default:
		return cs.sendStratumError(req.Id, &ErrorReply{Code: 20, Message: "Method not found"})
	}
}

func (cs *Session) sendStratumNotification(method string, params interface{}) error {
	cs.Lock()
	defer cs.Unlock()
	msg := StratumPushMessage{Id: nil, Method: method, Params: params}
	return cs.enc.Encode(&msg)
}

// sendStratumError returns an error response without tearing down the TCP
// session. Rejected shares are normal Stratum traffic and must not disconnect
// an ASIC.
func (cs *Session) sendStratumResult(id json.RawMessage, result interface{}) error {
	cs.Lock()
	defer cs.Unlock()
	message := StratumResponse{Id: id, Result: result, Error: nil}
	return cs.enc.Encode(&message)
}

func (cs *Session) sendStratumError(id json.RawMessage, reply *ErrorReply) error {
	cs.Lock()
	defer cs.Unlock()
	message := StratumResponse{Id: id, Result: nil, Error: []interface{}{reply.Code, reply.Message, nil}}
	return cs.enc.Encode(&message)
}

func (cs *Session) pushForgeDifficulty(diff int64) error {
	return cs.sendStratumNotification("mining.set_difficulty", []interface{}{diff})
}

func (cs *Session) pushForgeJob(job *forgeJob) error {
	return cs.sendStratumNotification("mining.notify", forgeNotifyParams(job))
}

// disconnectForgeMinersForPause closes every active FORGE Stratum miner
// connection when all external revenue chains are unavailable.
//
// There is no valid mining.notify replacement for a full FORGE pause. Closing
// existing miner sockets prevents them from continuing to hash and submit the
// previously invalidated job. Miners may reconnect while paused, but they will
// receive no work until a valid external participation mode is available again.
//
// sessionsMu is never held while closing network sockets.
func (s *ProxyServer) disconnectForgeMinersForPause() int {
	s.sessionsMu.RLock()
	sessions := make([]*Session, 0, len(s.sessions))
	for cs := range s.sessions {
		sessions = append(sessions, cs)
	}
	s.sessionsMu.RUnlock()

	disconnected := 0

	for _, cs := range sessions {
		cs.forgeStateMu.RLock()
		ready := cs.subscribed && cs.authorized
		cs.forgeStateMu.RUnlock()

		if !ready || cs.conn == nil {
			continue
		}

		// Closing the socket causes serveTCPClient to leave its read loop.
		// removeSession is intentionally also called here so a later recovery
		// broadcast cannot race against a socket already being torn down.
		_ = cs.conn.Close()
		s.removeSession(cs)
		disconnected++
	}

	return disconnected
}

func (s *ProxyServer) broadcastForgeJobs() {
	if s.forge == nil || s.isSick() {
		return
	}
	job := s.forge.currentJob()
	if job == nil {
		return
	}

	s.sessionsMu.RLock()
	sessions := make([]*Session, 0, len(s.sessions))
	for cs := range s.sessions {
		sessions = append(sessions, cs)
	}
	s.sessionsMu.RUnlock()

	log.Printf("FORGE: broadcasting merged Scrypt job %s to %d miners", job.ID, len(sessions))
	for _, cs := range sessions {
		// Snapshot each session outside sessionsMu. State locks cover only
		// memory updates, never miner, Redis or chain network calls.
		cs.forgeStateMu.RLock()
		ready, login := cs.subscribed && cs.authorized, cs.login
		cs.forgeStateMu.RUnlock()
		if !ready {
			continue
		}
		if err := cs.pushForgeWork(s, job); err != nil {
			log.Printf("FORGE job push failed to %s@%s: %v", login, cs.ip, err)
			s.removeSession(cs)
		}
	}
}
