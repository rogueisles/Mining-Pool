package storage

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

// ForgeShare is an immutable accounting receipt, journaled after all chain
// submissions. External outcomes are submission acknowledgements, not rewards.
type ForgeShare struct {
	ID              string `json:"id"`
	Login           string `json:"login"`
	Worker          string `json:"worker"`
	Height          int64  `json:"height"`
	Difficulty      int64  `json:"difficulty"`
	RoundDifficulty int64  `json:"roundDifficulty"`
	TimestampMS     int64  `json:"timestampMs"`
	WindowSeconds   int64  `json:"windowSeconds"`
	WorkHash        string `json:"workHash"`
	// ProofHash is Keccak256 of the exact accepted OGGF envelope. Empty means
	// no acknowledged native block and must never close an OGG payout round.
	ProofHash string          `json:"proofHash,omitempty"`
	Digibyte  ForgeSubmission `json:"digibyte"`
	Dogecoin  ForgeSubmission `json:"dogecoin"`
	Pepecoin  ForgeSubmission `json:"pepecoin"`
	Dingocoin ForgeSubmission `json:"dingocoin"`
	Bellscoin ForgeSubmission `json:"bellscoin"`
	OGGError  string          `json:"oggError,omitempty"`
}

type ForgeSubmission struct {
	Observation string `json:"observation,omitempty"`
	Attempted   bool   `json:"attempted"`
	Accepted    bool   `json:"accepted"`
	BlockHash   string `json:"blockHash,omitempty"`
	Error       string `json:"error,omitempty"`
}

// ForgeRoundID occupies the legacy nonce column and survives candidate ->
// immature -> mature serialization. It is NOT an Ethereum nonce.
func ForgeRoundID(proofHash string) string {
	return "forge-" + strings.TrimPrefix(strings.ToLower(proofHash), "0x")
}

func IsForgeRoundID(s string) bool {
	return strings.HasPrefix(s, "forge-")
}

func ForgeProofHash(blob []byte) string { return crypto.Keccak256Hash(blob).Hex() }

func validHash(s string, prefixed bool) bool {
	if prefixed {
		if !strings.HasPrefix(s, "0x") {
			return false
		}
		s = s[2:]
	}
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32
}

func (s ForgeShare) Validate() error {
	const maxExact = int64(1<<53 - 1)
	if !validHash(s.ID, false) || !validHash(s.WorkHash, true) ||
		(s.ProofHash != "" && !validHash(s.ProofHash, true)) {
		return errors.New("invalid FORGE receipt identity")
	}
	if s.Login == "" || s.Worker == "" || strings.ContainsAny(s.Login+s.Worker, ":\r\n") {
		return errors.New("invalid FORGE receipt account")
	}
	if s.Height <= 0 || s.Height > maxExact || s.Difficulty <= 0 || s.Difficulty > maxExact ||
		s.RoundDifficulty <= 0 || s.TimestampMS <= 0 || s.TimestampMS > maxExact ||
		s.WindowSeconds <= 0 || s.WindowSeconds > 365*24*3600 {
		return errors.New("invalid FORGE receipt quantities")
	}
	for _, result := range []ForgeSubmission{s.Digibyte, s.Dogecoin, s.Pepecoin, s.Dingocoin, s.Bellscoin} {
		if result.Observation != "" && (result.Observation != "noncanonical" || !result.Attempted || result.Accepted || result.Error == "") {
			return errors.New("invalid external observation")
		}
		if result.Accepted && (!result.Attempted || result.Error != "") {
			return errors.New("invalid external submission outcome")
		}
		if result.Attempted && !validHash(result.BlockHash, true) {
			return errors.New("invalid external block hash")
		}
	}
	return nil
}

// One script makes duplicate detection, share credit, native round closure,
// candidate creation and external outcome recording indivisible to clients.
// Preflight types/counters before writes: Redis scripts do not roll back a
// runtime error. Dedup receipts deliberately have no TTL; delayed journal
// replay must not pay again. Retention/archival is a separate operational gate.
const writeForgeShareScript = forgePackedMembership + `
local types = {'hash','hash','zset','zset','hash','hash','zset','hash','zset','hash','hash','string','hash','hash','hash'}
for i,t in ipairs(types) do
  local actual = redis.call('TYPE', KEYS[i]).ok
  if actual ~= 'none' and actual ~= t then return redis.error_reply('FORGE wrong key type '..i) end
end
if redis.call('HEXISTS', KEYS[1], ARGV[1]) == 1 then return 1 end
local compact=redis.call('GET',KEYS[12]) or ''
if #compact % 64 ~= 0 or #compact > 262144 then return redis.error_reply('FORGE corrupt compact bucket') end
if contains(compact,ARGV[1]) then return 1 end
local native = ARGV[9] ~= ''
if native and redis.call('EXISTS', KEYS[8]) == 1 then return redis.error_reply('FORGE round already exists') end
local diff = tonumber(ARGV[4])
local limit = 9007199254740991
local function checked(v, delta)
  if not v then v = '0' end
  if not string.match(v, '^%d+$') then return nil end
  local n = tonumber(v)
  if not n or n > limit-delta then return nil end
  if string.format('%.0f', n) ~= v then return nil end
  return n+delta
end
if not checked(redis.call('HGET', KEYS[2], ARGV[2]), diff) then return redis.error_reply('FORGE invalid miner shares') end
if not checked(redis.call('HGET', KEYS[6], 'roundShares'), diff) then return redis.error_reply('FORGE invalid round shares') end
local total = diff
if native then
  for _,v in ipairs(redis.call('HVALS', KEYS[2])) do
    total = checked(v, total)
    if not total then return redis.error_reply('FORGE invalid round total') end
  end
  if not checked(redis.call('HGET', KEYS[5], 'blocksFound'), 1) then return redis.error_reply('FORGE invalid block count') end
  local score = tonumber(redis.call('ZSCORE', KEYS[7], ARGV[2]) or '0')
  if not score or score < 0 or score >= limit then return redis.error_reply('FORGE invalid finder count') end
end
local ts = math.floor(tonumber(ARGV[5])/1000)
redis.call('HINCRBY', KEYS[2], ARGV[2], ARGV[4])
redis.call('ZADD', KEYS[3], ts, ARGV[4]..':'..ARGV[2]..':'..ARGV[3]..':'..ARGV[5]..':'..ARGV[1])
redis.call('ZADD', KEYS[4], ts, ARGV[4]..':'..ARGV[3]..':'..ARGV[5]..':'..ARGV[1])
redis.call('EXPIRE', KEYS[4], ARGV[6])
redis.call('HSET', KEYS[5], 'lastShare', ts)
if native then
  redis.call('HSET', KEYS[6], 'lastBlockFound', ts)
  redis.call('HDEL', KEYS[6], 'roundShares')
  redis.call('ZINCRBY', KEYS[7], 1, ARGV[2])
  redis.call('HINCRBY', KEYS[5], 'blocksFound', 1)
  redis.call('RENAME', KEYS[2], KEYS[8])
  local row = ARGV[10]..':'..ARGV[8]..':'..ARGV[9]..':'..string.format('%.0f',ts)..':'..ARGV[11]..':'..string.format('%.0f',total)
  redis.call('ZADD', KEYS[9], ARGV[7], row)
else
  redis.call('HINCRBY', KEYS[6], 'roundShares', ARGV[4])
end
if ARGV[12] ~= '' then redis.call('HSET', KEYS[10], ARGV[1], ARGV[12]) end
if ARGV[13] ~= '' then redis.call('HSET', KEYS[11], ARGV[1], ARGV[13]) end
if ARGV[14]~='' then redis.call('HSET',KEYS[13],ARGV[1],ARGV[14]) end
if ARGV[15]~='' then redis.call('HSET',KEYS[14],ARGV[1],ARGV[15]) end
if ARGV[16]~='' then redis.call('HSET',KEYS[15],ARGV[1],ARGV[16]) end
redis.call('HSET', KEYS[1], ARGV[1], '1')
return 0
`

func (r *RedisClient) WriteForgeShare(s ForgeShare) (bool, error) {
	if err := s.Validate(); err != nil {
		return false, err
	}
	encode := func(outcome ForgeSubmission) string {
		if !outcome.Attempted {
			return ""
		}
		blob, _ := json.Marshal(struct {
			ForgeSubmission
			Login       string `json:"login"`
			Worker      string `json:"worker"`
			TimestampMS int64  `json:"timestampMs"`
		}{outcome, s.Login, s.Worker, s.TimestampMS})
		return string(blob)
	}
	keys := []string{
		r.formatKey("forge", "credited", s.Height), r.formatKey("shares", "roundCurrent"),
		r.formatKey("hashrate"), r.formatKey("hashrate", s.Login), r.formatKey("miners", s.Login),
		r.formatKey("stats"), r.formatKey("finders"), r.formatRound(s.Height, ForgeRoundID(s.ProofHash)),
		r.formatKey("blocks", "candidates"), r.formatKey("forge", "submissions", "dgb", s.Height),
		r.formatKey("forge", "submissions", "doge", s.Height),
		r.forgeDedupArchiveKey(s.Height, s.ID[:2]),
		r.formatKey("forge", "submissions", "pep", s.Height), r.formatKey("forge", "submissions", "dingo", s.Height), r.formatKey("forge", "submissions", "bells", s.Height),
	}
	i := func(v int64) string { return strconv.FormatInt(v, 10) }
	args := []string{s.ID, s.Login, s.Worker, i(s.Difficulty), i(s.TimestampMS), i(s.WindowSeconds),
		i(s.Height), s.WorkHash, s.ProofHash, ForgeRoundID(s.ProofHash), i(s.RoundDifficulty), encode(s.Digibyte), encode(s.Dogecoin), encode(s.Pepecoin), encode(s.Dingocoin), encode(s.Bellscoin)}
	val, err := r.client.Eval(writeForgeShareScript, keys, args).Result()
	if err != nil {
		return false, err
	}
	n, ok := val.(int64)
	if !ok || (n != 0 && n != 1) {
		return false, fmt.Errorf("unexpected FORGE accounting reply %v", val)
	}
	return n == 1, nil
}

// Update external submission observations after credit, without touching shares,
// hashrate, native candidates or rounds. This is replay-safe after Redis ACK loss.
func (r *RedisClient) WriteForgeExternalOutcomes(s ForgeShare) error {
	if err := s.Validate(); err != nil {
		return err
	}
	encode := func(v ForgeSubmission) string {
		if !v.Attempted {
			return ""
		}
		blob, _ := json.Marshal(struct {
			ForgeSubmission
			Login       string `json:"login"`
			Worker      string `json:"worker"`
			TimestampMS int64  `json:"timestampMs"`
		}{v, s.Login, s.Worker, s.TimestampMS})
		return string(blob)
	}
	keys := []string{r.formatKey("forge", "credited", s.Height), r.forgeDedupArchiveKey(s.Height, s.ID[:2]), r.formatKey("forge", "submissions", "dgb", s.Height), r.formatKey("forge", "submissions", "doge", s.Height), r.formatKey("forge", "submissions", "pep", s.Height), r.formatKey("forge", "submissions", "dingo", s.Height), r.formatKey("forge", "submissions", "bells", s.Height)}
	_, err := r.client.Eval(forgePackedMembership+`
local types={'hash','string','hash','hash','hash','hash','hash'}
for i,t in ipairs(types) do
 local actual=redis.call('TYPE',KEYS[i]).ok
 if actual~='none' and actual~=t then return redis.error_reply('FORGE wrong outcome key type') end
end
local compact=redis.call('GET',KEYS[2]) or ''
if #compact%64~=0 or #compact>262144 then return redis.error_reply('FORGE corrupt compact bucket') end
if redis.call('HEXISTS',KEYS[1],ARGV[1])~=1 and not contains(compact,ARGV[1]) then
 return redis.error_reply('FORGE external update requires credited share')
end
if ARGV[2]~='' then redis.call('HSET',KEYS[3],ARGV[1],ARGV[2]) end
if ARGV[3]~='' then redis.call('HSET',KEYS[4],ARGV[1],ARGV[3]) end
if ARGV[4]~='' then redis.call('HSET',KEYS[5],ARGV[1],ARGV[4]) end
if ARGV[5]~='' then redis.call('HSET',KEYS[6],ARGV[1],ARGV[5]) end
if ARGV[6]~='' then redis.call('HSET',KEYS[7],ARGV[1],ARGV[6]) end
return 0`, keys, []string{s.ID, encode(s.Digibyte), encode(s.Dogecoin), encode(s.Pepecoin), encode(s.Dingocoin), encode(s.Bellscoin)}).Result()
	return err
}
