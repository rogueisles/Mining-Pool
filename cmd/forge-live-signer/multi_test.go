package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/ethereum/go-ethereum/common"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
)

func TestMultiSignerIndependentAvailabilityAndTreasury(t *testing.T) {
	testMultiSignerPolicy(t, "", []string{"DGB", "DOGE", "PEP", "DINGO", "BELLS"}, 31)
}

func TestMultiSignerTwoChainAvailabilityAndTreasury(t *testing.T) {
	testMultiSignerPolicy(t, forgepool.ExternalChainPolicyDGBDOGE, []string{"DGB", "DOGE"}, 3)
}

func TestV4SignerTwoChainAvailabilityAndNativeProposal(t *testing.T) {
	for _, witness := range []bool{false, true} {
		t.Run(fmt.Sprintf("witness-%v", witness), func(t *testing.T) {
			testMultiSignerPolicy(t, forgepool.ExternalChainPolicyDGBDOGE, []string{"DGB", "DOGE"}, 3, true, witness)
		})
	}
}

func TestV4FastHealthKeepsTreasuryAndTipValidation(t *testing.T) {
	for _, witness := range []bool{false, true} {
		t.Run(fmt.Sprintf("witness-%v", witness), func(t *testing.T) {
			testMultiSignerPolicy(t, forgepool.ExternalChainPolicyDGBDOGE, []string{"DGB", "DOGE"}, 3, true, witness, true)
		})
	}
}

func TestRegistryV5SignerNativeValidation(t *testing.T) {
	testMultiSignerPolicy(t, forgepool.ExternalChainPolicyRegistry, []string{"DGB", "DOGE", "SOQ"}, 35, true, false, false, true)
}

func testMultiSignerPolicy(t *testing.T, policy string, symbols []string, fullMask uint8, certificateOptions ...bool) {
	t.Helper()
	v5 := len(certificateOptions) > 3 && certificateOptions[3]
	v4 := len(certificateOptions) > 0 && certificateOptions[0]
	witness := len(certificateOptions) > 1 && certificateOptions[1]
	fastHealth := len(certificateOptions) > 2 && certificateOptions[2]
	witnessFirst := sha256.Sum256(make([]byte, 64))
	witnessDigest := sha256.Sum256(witnessFirst[:])
	witnessScript := append([]byte{0x6a, 0x24, 0xaa, 0x21, 0xa9, 0xed}, witnessDigest[:]...)
	var active uint32 = uint32(fullMask)
	var rejectProposal uint32
	var proposals uint32
	var certificateProposals uint32
	var claimProbes uint32
	var changeTipAfterProbe uint32
	now := uint32(time.Now().Unix())
	scripts := map[string]string{"DGB": "0014706267a5255e3666936bcb9a777667599f387546", "DOGE": "76a9144b1508ebe06723842994adf33bdba36b4eba44c488ac", "PEP": "76a914bb0ff0797074c03e9969359b605997b53c9eaa8488ac", "DINGO": "76a914e306e2a1a240f634da707d31079f18431317d7af88ac", "BELLS": "0014088a288ee425094ccd67779afd0c2016d010368c", "SOQ": "51205dbc511fd411101f2baee0fd1914568c5437b01693b100c7786a3d19809c7596"}
	c := validConfig()
	c.ExternalChainPolicy = policy
	if fullMask&32 != 0 {
		c.OptionalSOQBlock = 1125600
	}
	c.FastDigibyteHealth = fastHealth
	key, _ := crypto.HexToECDSA(strings.Repeat("0", 63) + "1")
	for _, symbol := range symbols {
		name := symbol
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID     interface{}
				Method string
				Params []json.RawMessage
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				w.WriteHeader(400)
				return
			}
			if uint8(atomic.LoadUint32(&active))&multiBits[name] == 0 {
				w.WriteHeader(503)
				return
			}
			var result interface{}
			switch req.Method {
			case "getchainstates":
				result = map[string]interface{}{"headers": 300000, "chainstates": []interface{}{map[string]interface{}{"blocks": 300000, "bestblockhash": strings.Repeat("11", 32), "validated": true}}}
			case "getblockchaininfo":
				result = map[string]interface{}{"chain": "main", "blocks": 300000, "headers": 300000, "initialblockdownload": false, "bestblockhash": strings.Repeat("11", 32)}
			case "getblockhash":
				var h int
				json.Unmarshal(req.Params[0], &h)
				if h == 104679 && name == "DOGE" {
					result = "35eb87ae90d44b98898fec8c39577b76cb1eb08e1261cfc10706c8ce9a1d01cf"
				} else if h == 1 && name == "DINGO" {
					result = "594a42d8fe16382085dc982135df72cf8fcea12d34e6efd566e2f9e442e2136f"
				} else {
					result = multiGenesis[name]
				}
			case "validateaddress":
				result = map[string]interface{}{"isvalid": true, "isdilithium": true, "address": "sq1ptk79z875zygp72awur73j9zk332r0vqkjwcsp3mcdg73nqyuwktq87gczj", "scriptPubKey": scripts[name]}
			case "getnetworkinfo":
				result = map[string]interface{}{"connections": 8}
			case "getblockheader":
				result = map[string]interface{}{"time": now}
			case "getbestblockhash":
				result = strings.Repeat("11", 32)
				if atomic.LoadUint32(&changeTipAfterProbe) != 0 && atomic.LoadUint32(&claimProbes) != 0 {
					result = strings.Repeat("22", 32)
				}
			case "getblocktemplate":
				var p map[string]interface{}
				json.Unmarshal(req.Params[0], &p)
				if p["mode"] == "proposal" {
					atomic.AddUint32(&proposals, 1)
					data, _ := p["data"].(string)
					block, err := hex.DecodeString(data)
					if err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					claim := nativeFixtureClaim(t, block, name != "DGB")
					if claim > 100000000 {
						result = "bad-cb-amount"
						atomic.AddUint32(&claimProbes, 1)
					}
					if v4 && name == "DGB" {
						if bytes.Contains(block, []byte("RGNCERT4")) || bytes.Contains(block, []byte("RGNCERT5")) {
							checkV4NativeProposal(t, block, scripts["DGB"], witness, claim)
							atomic.AddUint32(&certificateProposals, 1)
						}
					}
					if atomic.LoadUint32(&rejectProposal)&uint32(multiBits[name]) != 0 {
						result = "bad-txns"
					}
				} else {
					result = map[string]interface{}{"version": 4, "previousblockhash": strings.Repeat("11", 32), "height": 300001, "coinbasevalue": 100000000, "curtime": now, "mintime": now - 1, "bits": "207fffff", "transactions": []interface{}{}, "pow_algo": "scrypt", "pow_algo_id": 1}
					if witness && name == "DGB" {
						result.(map[string]interface{})["default_witness_commitment"] = hex.EncodeToString(witnessScript)
					}
				}
			default:
				t.Errorf("unexpected RPC %s", req.Method)
				w.WriteHeader(400)
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"id": req.ID, "result": result, "error": nil})
		}))
		defer server.Close()
		c.Multi = append(c.Multi, multiEndpoint{Symbol: name, ChainID: multiIDs[name], PayoutAddress: "sq1ptk79z875zygp72awur73j9zk332r0vqkjwcsp3mcdg73nqyuwktq87gczj", PayoutScript: scripts[name], endpoint: endpoint{URL: server.URL, Username: "pilot", Password: "test-only", Genesis: multiGenesis[name]}})
	}
	var registry forgepool.RegistrySnapshot
	parentHash := common.HexToHash("0xabc")
	if v5 {
		c.OptionalSOQBlock = 0
		c.RegistryBlock = 1125600
		c.RegistryChainID = 70088
		admin, _ := crypto.HexToECDSA(strings.Repeat("0", 62) + "10")
		registry = forgepool.RegistrySnapshot{Admin: forgepool.RegistryAdmin{Keys: []common.Address{crypto.PubkeyToAddress(admin.PublicKey)}, Quorum: 1, Epoch: 1}}
		for i, symbol := range []string{"DGB", "DOGE", "SOQ"} {
			script, _ := hex.DecodeString(scripts[symbol])
			profile := forgepool.RegistryProfileBitcoin80
			if symbol == "SOQ" {
				profile = forgepool.RegistryProfileSOQ
			}
			registry.Coins = append(registry.Coins, forgepool.RegistryCoin{Slot: uint8(i + 1), ChainID: multiIDs[symbol], Genesis: common.HexToHash(multiGenesis[symbol]), Profile: profile, Core: true, EnabledFrom: 1125600, Treasury: common.Hash(sha256.Sum256(script))})
		}
		root, _ := forgepool.RegistrySnapshotRoot(registry)
		raw, _ := rlp.EncodeToBytes(registry)
		rgn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 0, "error": nil, "result": map[string]interface{}{"parentHash": parentHash, "parentHeight": fmt.Sprintf("0x%x", 1125599), "activationBlock": fmt.Sprintf("0x%x", 1125600), "root": root, "checkpoint": "0x0", "snapshotRlp": "0x" + hex.EncodeToString(raw), "genesis": common.HexToHash("0x9876"), "chainId": "0x111c8", "workSigners": []common.Address{crypto.PubkeyToAddress(key.PublicKey)}, "minDelay": "0x2", "minOperationInterval": "0x2"}})
		}))
		t.Cleanup(rgn.Close)
		c.RegistryRPC = endpoint{URL: rgn.URL, Username: "test", Password: "test-only", Genesis: strings.TrimPrefix(common.HexToHash("0x9876").Hex(), "0x")}
	}
	if e := validateMultiConfig(c); e != nil {
		t.Fatal(e)
	}
	fresh := func() *multiService {
		s := &multiService{cfg: c, key: key, health: map[string]*multiHealth{}}
		for name := range multiBits {
			s.health[name] = &multiHealth{}
		}
		return s
	}
	var lastJob *forgepool.DigibyteJob
	request := func(mask uint8, values ...map[string]int64) forgepool.MultiSignRequest {
		aux := map[uint32]*forgepool.DogeCandidate{}
		p := forgepool.ParentTemplate{Version: 4, Previous: forgepool.Hash{}, Height: 300001, Bits: 0x207fffff, CurTime: now, CoinbaseValue: 100000000}
		for i := range p.Previous {
			p.Previous[i] = 0x11
		}
		req := forgepool.MultiSignRequest{WorkHash: common.HexToHash("0x1234"), AuxNonce: 7}
		if fullMask == 35 {
			previous := common.Hash{}
			if v5 {
				previous = parentHash
			}
			h := []interface{}{previous, common.Hash{}, common.Address{}, common.Hash{}, common.Hash{}, common.Hash{}, [256]byte{}, big.NewInt(1), big.NewInt(1125600), uint64(8000000), uint64(0), uint64(now), []byte{}, common.Hash{}, [8]byte{}}
			req.RGNHeader, _ = rlp.EncodeToBytes(h)
			req.WorkHash = crypto.Keccak256Hash(req.RGNHeader)
		}
		for _, ep := range c.Multi {
			if ep.ChainID == 0 || mask&multiBits[ep.Symbol] == 0 {
				continue
			}
			script, _ := hex.DecodeString(ep.PayoutScript)
			candidateTemplate := p
			if len(values) != 0 {
				if amount, ok := values[0][ep.Symbol]; ok {
					candidateTemplate.CoinbaseValue = amount
				}
			}
			candidate, e := forgepool.BuildAuxCandidate(candidateTemplate, script, 7, ep.ChainID)
			if e != nil {
				t.Fatal(e)
			}
			aux[ep.ChainID] = candidate
			req.Aux = append(req.Aux, forgepool.MultiEvidence{ChainID: ep.ChainID, Candidate: candidateTemplate})
		}
		script, _ := hex.DecodeString(scripts["DGB"])
		if mask&1 != 0 {
			if len(values) != 0 {
				if amount, ok := values[0]["DGB"]; ok {
					p.CoinbaseValue = amount
				}
			}
			if witness {
				p.WitnessCommitment = witnessScript
			}
			copy := p
			req.DGB = &copy
		} else {
			p, _ = forgepool.SyntheticParentTemplate(1, now)
			script = []byte{0x6a}
		}
		builder := forgepool.BuildMultiParent
		if v4 {
			builder = forgepool.BuildMultiParentV4
		}
		if v5 {
			builder = forgepool.BuildMultiParentV5
		}
		job, e := builder(p, req.WorkHash.Hex(), aux, script, 7, 4, 4, mask&1 != 0)
		if e != nil {
			t.Fatal(e)
		}
		if v5 {
			root, _ := forgepool.RegistrySnapshotRoot(registry)
			req.Registry = &forgepool.RegistryEnvelope{Root: root}
			for _, coin := range registry.Coins {
				bit := multiBits[map[uint8]string{1: "DGB", 2: "DOGE", 3: "SOQ"}[coin.Slot]]
				if mask&bit != 0 {
					m, _ := forgepool.RegistryMembershipFor(registry, coin.Slot)
					req.Registry.Members = append(req.Registry.Members, m)
				}
			}
			if err := job.BindRegistry(req.Registry); err != nil {
				t.Fatal(err)
			}
		}
		req.Template, e = job.MultiTemplate(uint64(now), uint64(now)+300)
		if e != nil {
			t.Fatal(e)
		}
		lastJob = job
		return req
	}
	for mask := uint8(1); mask <= fullMask; mask++ {
		if mask & ^fullMask != 0 {
			continue
		}
		t.Run(fmt.Sprintf("mask-%02x", mask), func(t *testing.T) {
			atomic.StoreUint32(&active, uint32(mask))
			s := fresh()
			req := request(mask)
			for i := 0; i < 3; i++ {
				sig, e := s.validateAndSign(context.Background(), req)
				if mask&35 == 0 {
					if e == nil || len(sig) != 0 {
						t.Fatal("bonus-only mask signed", mask)
					}
				} else if i < 2 && len(req.RGNHeader) == 0 && mask&(fullMask&^32) != fullMask&^32 {
					if e == nil {
						t.Fatal("reduced work authorized before independent exclusion")
					}
				} else if e != nil || len(sig) != 65 {
					t.Fatal("nonempty independently observed subset refused", e)
				} else if v4 {
					work, err := forgepool.MultiRequestWork(req)
					if err != nil {
						t.Fatal(err)
					}
					digest, err := forgepool.MultiDigest(req.Template, work)
					if err != nil {
						t.Fatal(err)
					}
					public, err := crypto.SigToPub(digest[:], sig)
					if err != nil || crypto.PubkeyToAddress(*public) != crypto.PubkeyToAddress(key.PublicKey) {
						t.Fatal("wrong V4 signer domain", err)
					}
					if err := lastJob.BindCertificate(req.Template, [][]byte{sig}); err != nil {
						t.Fatal(err)
					}
					if err := lastJob.CheckCertificate(req.Template, [][]byte{sig}); err != nil {
						t.Fatal(err)
					}
					proof, err := lastJob.BuildMultiProof(make([]byte, 4), make([]byte, 4), lastJob.Template.CurTime, 0, req.Template, [][]byte{sig})
					if err != nil || (proof.Template.Version != 4 && !v5) || (v5 && proof.Template.Version != 5) {
						t.Fatal("validated V4 request did not produce bound proof", err)
					}
					if mask == 2 && (len(lastJob.Coinbase.Outputs) != 2 || lastJob.Coinbase.Outputs[0].Value != 0 || lastJob.Coinbase.Outputs[1].Value != 0) {
						t.Fatal("synthetic carrier changed native reward outputs")
					}
				}
			}
		})
	}
	atomic.StoreUint32(&active, uint32(fullMask))
	s := fresh()
	req := request(fullMask)
	atomic.StoreUint32(&rejectProposal, 1)
	if _, e := s.validateAndSign(context.Background(), req); e == nil {
		t.Fatal("native-invalid body signed")
	}
	atomic.StoreUint32(&rejectProposal, 0)
	wrong := req
	wrong.Template.ParentCoinbaseHash[0] ^= 1
	if _, e := s.validateAndSign(context.Background(), wrong); e == nil {
		t.Fatal("unvalidated parent body signed")
	}
	// The pool cannot omit an available chain from the signed template.
	if _, e := s.validateAndSign(context.Background(), request(2)); (e == nil) != (fullMask == 35) {
		t.Fatal("healthy source selection policy differs", fullMask, e)
	}
	// A responsive node with an unusable treasury-paying native body must not
	// prevent the remaining chains from carrying shared work.
	rejected, remaining := uint32(16), uint8(15)
	if fullMask == 3 || fullMask == 35 {
		rejected, remaining = 1, 2
	}
	atomic.StoreUint32(&rejectProposal, rejected)
	for i := 0; i < 3; i++ {
		_, e := s.validateAndSign(context.Background(), request(remaining))
		if i < 2 && fullMask != 35 && e == nil {
			t.Fatal("native-body exclusion happened too early")
		}
		if i == 2 && e != nil {
			t.Fatal("usable chains blocked by a native-invalid body", e)
		}
	}
	atomic.StoreUint32(&rejectProposal, 0)
	atomic.StoreUint32(&active, 0)
	for i := 0; i < 3; i++ {
		if _, e := s.validateAndSign(context.Background(), forgepool.MultiSignRequest{}); e == nil {
			t.Fatal("zero-chain probe signed")
		}
	}
	atomic.StoreUint32(&active, 2)
	if _, e := s.validateAndSign(context.Background(), request(2)); !v5 && e == nil {
		t.Fatal("restored before two successful observations")
	}
	if _, e := s.validateAndSign(context.Background(), request(2)); e != nil {
		t.Fatal("single restored chain refused", e)
	}
	if atomic.LoadUint32(&proposals) == 0 {
		t.Fatal("native body proposal checks bypassed")
	}
	if v4 && atomic.LoadUint32(&certificateProposals) == 0 {
		t.Fatal("requested V4 parent never checked by native proposal RPC")
	}
	if atomic.LoadUint32(&claimProbes) == 0 {
		t.Fatal("native amount boundary never checked")
	}
	if v4 && fullMask == 3 {
		t.Run("HTTP-outage-and-recovery", func(t *testing.T) {
			atomic.StoreUint32(&active, uint32(fullMask))
			atomic.StoreUint32(&rejectProposal, 0)
			atomic.StoreUint32(&changeTipAfterProbe, 0)
			service := fresh()
			post := func(req forgepool.MultiSignRequest) int {
				body, err := json.Marshal(req)
				if err != nil {
					t.Fatal(err)
				}
				writer := httptest.NewRecorder()
				service.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
				return writer.Code
			}
			if post(request(fullMask)) != http.StatusOK {
				t.Fatal("healthy HTTP signer refused work")
			}
			atomic.StoreUint32(&active, 0)
			before := atomic.LoadUint32(&proposals)
			for attempt := 0; attempt < 3; attempt++ {
				if post(forgepool.MultiSignRequest{}) != http.StatusBadRequest {
					t.Fatal("empty public probe reached native validation")
				}
			}
			if atomic.LoadUint32(&proposals) != before {
				t.Fatal("empty public probe issued a native proposal")
			}
			atomic.StoreUint32(&active, 2)
			for attempt := 0; attempt < 3; attempt++ {
				status := post(request(2))
				if (attempt < 2 && status != http.StatusUnprocessableEntity) || (attempt == 2 && status != http.StatusOK) {
					t.Fatal("HTTP single-source health did not converge", attempt, status)
				}
			}
			atomic.StoreUint32(&active, uint32(fullMask))
			if post(request(fullMask)) != http.StatusUnprocessableEntity || post(request(fullMask)) != http.StatusOK {
				t.Fatal("HTTP full-source recovery did not converge")
			}
		})
		atomic.StoreUint32(&active, uint32(fullMask))
		for _, symbol := range []string{"DGB", "DOGE"} {
			for _, amount := range []int64{0, 99999990, 99999999, 100000001} {
				s := fresh()
				req := request(fullMask, map[string]int64{symbol: amount})
				if sig, err := s.validateAndSign(context.Background(), req); err == nil || len(sig) != 0 {
					t.Fatalf("%s claim %d produced a signature", symbol, amount)
				}
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if sig, err := fresh().validateAndSign(ctx, request(fullMask)); err == nil || len(sig) != 0 {
			t.Fatal("cancelled authorization escaped")
		}
		atomic.StoreUint32(&changeTipAfterProbe, 1)
		if sig, err := fresh().validateAndSign(context.Background(), request(fullMask)); err == nil || len(sig) != 0 {
			t.Fatal("changed native tip produced a signature")
		}
	}
}

// Parse the one-coinbase fixture independently from the pool builder, preserving
// the distinction between its witness serialization and its txid serialization.
func checkV4NativeProposal(t *testing.T, block []byte, payoutHex string, witness bool, expectedClaim int64) {
	t.Helper()
	r := bytes.NewReader(block)
	read := func(n int) []byte {
		value := make([]byte, n)
		if _, err := io.ReadFull(r, value); err != nil {
			t.Fatal("truncated native proposal", err)
		}
		return value
	}
	compact := func() int {
		n := int(read(1)[0])
		if n >= 253 {
			t.Fatal("oversized compact fixture field")
		}
		return n
	}
	header := read(80)
	if compact() != 1 {
		t.Fatal("unexpected native proposal transaction count")
	}
	base := append([]byte{}, read(4)...)
	if witness && !bytes.Equal(read(2), []byte{0, 1}) {
		t.Fatal("missing witness serialization")
	}
	start := len(block) - r.Len()
	if compact() != 1 {
		t.Fatal("unexpected coinbase input count")
	}
	read(36)
	read(compact())
	read(4)
	outputs := compact()
	wantOutputs := 2
	if witness {
		wantOutputs++
	}
	if outputs != wantOutputs {
		t.Fatal("unexpected native coinbase outputs")
	}
	var certificateCount, treasuryCount, witnessCount int
	payout, _ := hex.DecodeString(payoutHex)
	for i := 0; i < outputs; i++ {
		value := binary.LittleEndian.Uint64(read(8))
		script := read(compact())
		if bytes.Contains(script, []byte("RGNCERT4")) || bytes.Contains(script, []byte("RGNCERT5")) {
			tag := []byte("RGNCERT4")
			if bytes.Contains(script, []byte("RGNCERT5")) {
				tag = []byte("RGNCERT5")
			}
			certificateCount++
			if value != 0 || len(script) != 42 || !bytes.Equal(script[:10], append([]byte{0x6a, 40}, tag...)) || !bytes.Equal(script[10:], make([]byte, 32)) {
				t.Fatal("invalid V4 proposal certificate placeholder")
			}
		}
		if bytes.Equal(script, payout) && value == uint64(expectedClaim) {
			treasuryCount++
		} else if value != 0 {
			t.Fatal("unexpected positive native coinbase payout")
		}
		if len(script) == 38 && bytes.HasPrefix(script, []byte{0x6a, 0x24, 0xaa, 0x21, 0xa9, 0xed}) && value == 0 {
			witnessCount++
			first := sha256.Sum256(make([]byte, 64))
			commitment := sha256.Sum256(first[:])
			if !bytes.Equal(script[6:], commitment[:]) {
				t.Fatal("invalid witness commitment for the empty witness tree")
			}
		}
	}
	end := len(block) - r.Len()
	base = append(base, block[start:end]...)
	if witness && (compact() != 1 || compact() != 32 || !bytes.Equal(read(32), make([]byte, 32))) {
		t.Fatal("invalid coinbase witness reserve")
	}
	base = append(base, read(4)...)
	first := sha256.Sum256(base)
	root := sha256.Sum256(first[:])
	if r.Len() != 0 || !bytes.Equal(header[36:68], root[:]) || certificateCount != 1 || treasuryCount != 1 || (witness && witnessCount != 1) {
		t.Fatal("native proposal body, Merkle root or payout differs")
	}
}

func nativeFixtureClaim(t *testing.T, block []byte, auxiliary bool) int64 {
	t.Helper()
	if len(block) < 80 {
		t.Error("short native block")
		return -1
	}
	r := bytes.NewReader(block[80:])
	if auxiliary {
		var parent wire.MsgTx
		if err := parent.Deserialize(r); err != nil {
			t.Error(err)
			return -1
		}
		if _, err := r.Seek(32, io.SeekCurrent); err != nil {
			t.Error(err)
			return -1
		}
		for i := 0; i < 2; i++ {
			n, err := wire.ReadVarInt(r, 0)
			if err != nil || n > 30 {
				t.Error("invalid native branch", err)
				return -1
			}
			if _, err := r.Seek(int64(n*32+4), io.SeekCurrent); err != nil {
				t.Error(err)
				return -1
			}
		}
		if _, err := r.Seek(80, io.SeekCurrent); err != nil {
			t.Error(err)
			return -1
		}
	}
	n, err := wire.ReadVarInt(r, 0)
	if err != nil || n == 0 {
		t.Error("missing coinbase", err)
		return -1
	}
	var coinbase wire.MsgTx
	if err := coinbase.Deserialize(r); err != nil {
		t.Error(err)
		return -1
	}
	var total int64
	for _, out := range coinbase.TxOut {
		total += out.Value
	}
	return total
}

func TestOptionalSOQSignerIndependentNativeValidation(t *testing.T) {
	testMultiSignerPolicy(t, forgepool.ExternalChainPolicyDGBDOGESQ, []string{"DGB", "DOGE", "SOQ"}, 35, true, true, true)
}
