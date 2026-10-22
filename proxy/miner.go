package proxy

import (
	"log"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

func cleanHex(s string) string {
	return strings.ToLower(strings.TrimPrefix(s, "0x"))
}

func sameHex(a, b string) bool {
	return cleanHex(a) == cleanHex(b)
}

func (s *ProxyServer) processShare(login, id, ip string, t *BlockTemplate, params []string) (bool, bool) {
	if t == nil {
		log.Printf("OGG share rejected: no current template from %v@%v params=%v", login, ip, params)
		return false, false
	}

	if len(params) < 3 {
		log.Printf("OGG share rejected: not enough params from %v@%v params=%v", login, ip, params)
		return false, false
	}

	nonceHex := params[0]
	hashNoNonce := strings.Replace(params[1], "0x", "", -1)
	mixDigest := params[2]
	nonce, _ := strconv.ParseUint(strings.Replace(nonceHex, "0x", "", -1), 16, 64)
	shareDiff := s.config.Proxy.Difficulty

	h, ok := t.headers[hashNoNonce]
	if !ok {
		log.Printf("OGG stale/unknown share from %v@%v", login, ip)
		log.Printf("OGG submitted header: %s", hashNoNonce)
		log.Printf("OGG current template header: %s height=%d", t.Header, t.Height)
		return false, false
	}

	_ = Block{
		number:      h.height,
		hashNoNonce: common.HexToHash(hashNoNonce),
		difficulty:  big.NewInt(shareDiff),
		nonce:       nonce,
		mixDigest:   common.HexToHash(mixDigest),
	}

	// Always write accepted pool share for miner accounting/hashrate.
	exist, err := s.backend.WriteShare(login, id, params, shareDiff, h.height, s.hashrateExpiration)
	if exist {
		return true, false
	}
	if err != nil {
		log.Println("OGG failed to insert share data into backend:", err)
		return false, false
	}

	// Submit to node, but DO NOT immediately WriteBlock just because submitWork returns true.
	// On OggPoW/custom chain submitWork can return true for work that later is not canonical.
	// Only write a block candidate if the canonical block at that height has the same nonce.
	go func(height uint64, diff *big.Int, submitParams []string, submittedNonce string) {
		ok, err := s.rpc().SubmitBlock(submitParams)
		if err != nil {
			log.Printf("OGG submitWork error at height %v: %v", height, err)
			return
		}
		if !ok {
			return
		}

		// Give node a short moment to import/propagate canonical block.
		time.Sleep(1200 * time.Millisecond)

		block, err := s.rpc().GetBlockByHeight(int64(height))
		if err != nil {
			log.Printf("OGG canonical check error at height %v: %v", height, err)
			return
		}
		if block == nil {
			log.Printf("OGG canonical check: no block at height %v yet", height)
			return
		}

		// Only now consider it a real pool-found block.
		if !sameHex(block.Nonce, submittedNonce) {
			log.Printf("OGG submitWork true but canonical nonce mismatch height=%d submitted=%s canonical=%s hash=%s",
				height, submittedNonce, block.Nonce, block.Hash)
			return
		}

		s.fetchBlockTemplate()

		// Store canonical block hash in pool backend.
		// submitParams[1] is the submitted work/header hash, but unlocker needs the real canonical block hash.
		canonicalParams := []string{submittedNonce, block.Hash, submitParams[2]}
		if block.MixHash != "" {
			canonicalParams[2] = block.MixHash
		}

		blockExist, err := s.backend.WriteBlock(login, id, canonicalParams, shareDiff, diff.Int64(), height, s.hashrateExpiration)
		if blockExist {
			return
		}
		if err != nil {
			log.Println("OGG failed to insert canonical block candidate into backend:", err)
			return
		}

		log.Printf("OGG BLOCK FOUND canonical by miner %v@%v height=%d nonce=%s hash=%s", login, ip, height, submittedNonce, block.Hash)
	}(h.height, h.diff, append([]string(nil), params...), nonceHex)

	return false, true
}
