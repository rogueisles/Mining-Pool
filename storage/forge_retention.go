package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const MaxForgeDedupBucketIDs = 4096

// Exact, permanent membership. No TTL, probabilistic filter or height cutoff.
// A compact bucket is a sorted string of fixed-width 64-character IDs.
const forgePackedMembership = `
local function contains(blob,id)
  if #blob % 64 ~= 0 or #blob > 262144 then return redis.error_reply('FORGE corrupt compact bucket') end
  local lo,hi=0,#blob/64-1
  while lo<=hi do
    local mid=math.floor((lo+hi)/2)
    local value=string.sub(blob,mid*64+1,mid*64+64)
    if value==id then return true end
    if value<id then lo=mid+1 else hi=mid-1 end
  end
  return false
end
`

type ForgeDedupPlan struct {
	Version   int      `json:"version"`
	Namespace string   `json:"namespace"`
	Height    int64    `json:"height"`
	Bucket    string   `json:"bucket"`
	Previous  string   `json:"previous"`
	LiveIDs   []string `json:"liveIDs"`
	Packed    string   `json:"packed"`
	Digest    string   `json:"digest"`
}

func (p ForgeDedupPlan) digest() string {
	p.Digest = ""
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func unpackForgeIDs(packed, bucket string) ([]string, error) {
	if len(packed)%64 != 0 || len(packed) > 64*MaxForgeDedupBucketIDs {
		return nil, errors.New("invalid compact bucket size")
	}
	ids := make([]string, 0, len(packed)/64)
	for i := 0; i < len(packed); i += 64 {
		id := packed[i : i+64]
		if !validHash(id, false) || !strings.HasPrefix(id, bucket) || (len(ids) > 0 && ids[len(ids)-1] >= id) {
			return nil, errors.New("invalid compact bucket order/identity")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (p ForgeDedupPlan) Validate() error {
	if p.Version != 1 || p.Namespace == "" || p.Height <= 0 || p.Height > 1<<53-1 || len(p.Bucket) != 2 || len(p.LiveIDs) > MaxForgeDedupBucketIDs {
		return errors.New("invalid dedup plan")
	}
	if _, err := hex.DecodeString(p.Bucket); err != nil {
		return err
	}
	old, err := unpackForgeIDs(p.Previous, p.Bucket)
	if err != nil {
		return err
	}
	if _, err := unpackForgeIDs(p.Packed, p.Bucket); err != nil {
		return err
	}
	ids := make(map[string]bool)
	for _, id := range old {
		ids[id] = true
	}
	for n, id := range p.LiveIDs {
		if !validHash(id, false) || !strings.HasPrefix(id, p.Bucket) || (n > 0 && p.LiveIDs[n-1] >= id) {
			return errors.New("invalid live dedup identities")
		}
		ids[id] = true
	}
	merged := make([]string, 0, len(ids))
	for id := range ids {
		merged = append(merged, id)
	}
	sort.Strings(merged)
	if strings.Join(merged, "") != p.Packed || p.Digest != p.digest() {
		return errors.New("dedup plan digest/content mismatch")
	}
	return nil
}

func (r *RedisClient) forgeDedupArchiveKey(height int64, bucket string) string {
	return r.formatKey("forge", "credited-compact", height, bucket)
}

// Planning is read-only. HSCAN may race normal credit: apply removes only the
// reviewed IDs, leaving any new entries live. A changed base bucket fails CAS.
func (r *RedisClient) PlanForgeDedup(height int64, bucket string) (*ForgeDedupPlan, error) {
	p := &ForgeDedupPlan{Version: 1, Namespace: r.prefix, Height: height, Bucket: bucket, LiveIDs: []string{}}
	p.Digest = p.digest()
	if err := p.Validate(); err != nil {
		return nil, err
	}
	key := r.formatKey("forge", "credited", height)
	value, err := r.client.Eval(`local v=redis.call('GET',KEYS[1]); if not v then return '' end; return v`, []string{r.forgeDedupArchiveKey(height, bucket)}, []string{}).Result()
	if err != nil {
		return nil, err
	}
	var ok bool
	p.Previous, ok = value.(string)
	if !ok {
		return nil, errors.New("invalid compact read")
	}
	old, err := unpackForgeIDs(p.Previous, bucket)
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool)
	cursor := "0"
	for {
		value, err = r.client.Eval(`return redis.call('HSCAN',KEYS[1],ARGV[1],'MATCH',ARGV[2]..'*','COUNT',256)`, []string{key}, []string{cursor, bucket}).Result()
		if err != nil {
			return nil, err
		}
		row, ok := value.([]interface{})
		if !ok || len(row) != 2 {
			return nil, errors.New("invalid dedup scan reply")
		}
		cursor, ok = row[0].(string)
		if !ok {
			return nil, errors.New("invalid dedup cursor")
		}
		fields, ok := row[1].([]interface{})
		if !ok || len(fields)%2 != 0 {
			return nil, errors.New("invalid dedup scan fields")
		}
		for n := 0; n < len(fields); n += 2 {
			id, a := fields[n].(string)
			v, b := fields[n+1].(string)
			if !a || !b || v != "1" || !validHash(id, false) || !strings.HasPrefix(id, bucket) {
				return nil, errors.New("invalid live dedup record")
			}
			live[id] = true
			if len(live) > MaxForgeDedupBucketIDs {
				return nil, errors.New("dedup bucket exceeds compaction bound")
			}
		}
		if cursor == "0" {
			break
		}
	}
	merged := make(map[string]bool)
	for _, id := range old {
		merged[id] = true
	}
	for id := range live {
		p.LiveIDs = append(p.LiveIDs, id)
		merged[id] = true
	}
	if len(merged) > MaxForgeDedupBucketIDs {
		return nil, errors.New("dedup bucket exceeds compaction bound")
	}
	sort.Strings(p.LiveIDs)
	all := make([]string, 0, len(merged))
	for id := range merged {
		all = append(all, id)
	}
	sort.Strings(all)
	p.Packed = strings.Join(all, "")
	p.Digest = p.digest()
	return p, p.Validate()
}

const applyForgeDedupScript = `
local live=redis.call('TYPE',KEYS[1]).ok
local compact=redis.call('TYPE',KEYS[2]).ok
if live~='none' and live~='hash' then return redis.error_reply('FORGE wrong live type') end
if compact~='none' and compact~='string' then return redis.error_reply('FORGE wrong compact type') end
local current=redis.call('GET',KEYS[2]) or ''
local same=current==ARGV[2]
if not same and current~=ARGV[1] then return redis.error_reply('FORGE compact plan conflict') end
for i=3,#ARGV do
  local v=redis.call('HGET',KEYS[1],ARGV[i])
  if v and v~='1' then return redis.error_reply('FORGE live record conflict') end
  if not same and not v then return redis.error_reply('FORGE missing reviewed record') end
end
redis.call('SET',KEYS[2],ARGV[2])
for i=3,#ARGV do redis.call('HDEL',KEYS[1],ARGV[i]) end
return #ARGV-2
`

func (r *RedisClient) ApplyForgeDedup(p ForgeDedupPlan) (int64, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	if p.Namespace != r.prefix {
		return 0, errors.New("dedup namespace mismatch")
	}
	args := append([]string{p.Previous, p.Packed}, p.LiveIDs...)
	result, err := r.client.Eval(applyForgeDedupScript, []string{r.formatKey("forge", "credited", p.Height), r.forgeDedupArchiveKey(p.Height, p.Bucket)}, args).Result()
	if err != nil {
		return 0, err
	}
	n, ok := result.(int64)
	if !ok {
		return 0, fmt.Errorf("unexpected dedup apply result")
	}
	return n, nil
}

func (p ForgeDedupPlan) Filename() string {
	return "dedup-" + strconv.FormatInt(p.Height, 10) + "-" + p.Bucket + "-" + p.Digest + ".json"
}
