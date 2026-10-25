package main

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

func adminTestKeys(start int64) []*ecdsa.PrivateKey {
	var keys []*ecdsa.PrivateKey
	for i := int64(0); i < 3; i++ {
		key, err := crypto.ToECDSA(common.LeftPadBytes(big.NewInt(start+i).Bytes(), 32))
		if err != nil {
			panic(err)
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := crypto.PubkeyToAddress(keys[i].PublicKey), crypto.PubkeyToAddress(keys[j].PublicKey)
		return bytes.Compare(a[:], b[:]) < 0
	})
	return keys
}
func adminTestAddresses(keys []*ecdsa.PrivateKey) []common.Address {
	var result []common.Address
	for _, key := range keys {
		result = append(result, crypto.PubkeyToAddress(key.PublicKey))
	}
	return result
}
func adminTestHash(n int64) common.Hash { return common.BigToHash(big.NewInt(n)) }

func adminTestFixture(t *testing.T) (string, rpc.ForgeRegistryReply, []*ecdsa.PrivateKey, []*ecdsa.PrivateKey) {
	t.Helper()
	dir := t.TempDir()
	keys, work := adminTestKeys(5001), adminTestKeys(6001)
	snapshot := forgepool.RegistrySnapshot{Admin: forgepool.RegistryAdmin{Keys: adminTestAddresses(keys), Quorum: 2, Epoch: 1}, Coins: []forgepool.RegistryCoin{
		{Slot: 1, ChainID: 0, Genesis: adminTestHash(100), Profile: forgepool.RegistryProfileBitcoin80, Core: true, Treasury: adminTestHash(1)},
		{Slot: 2, ChainID: 98, Genesis: adminTestHash(101), Profile: forgepool.RegistryProfileBitcoin80, Core: true, Treasury: adminTestHash(2)},
		{Slot: 3, ChainID: forgepool.SOQAuxChainID, Genesis: adminTestHash(102), Profile: forgepool.RegistryProfileSOQ, Core: true, Treasury: adminTestHash(3)},
	}}
	raw, err := rlp.EncodeToBytes(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	root, err := forgepool.RegistrySnapshotRoot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	state := rpc.ForgeRegistryReply{ParentHash: adminTestHash(999), ParentHeight: "0x63", ActivationBlock: "0x64", Root: root, Checkpoint: "0x0", SnapshotRLP: hexutil.Encode(raw), Genesis: adminTestHash(7008914), ChainID: "0x6af2d2", WorkSigners: adminTestAddresses(work), MinDelay: "0x2", MinOperationInterval: "0x1"}
	state.ChainID = hexutil.EncodeBig(big.NewInt(7008914))
	if err = writeJSON(filepath.Join(dir, "state.json"), map[string]interface{}{"jsonrpc": "2.0", "id": 1, "result": state}); err != nil {
		t.Fatal(err)
	}
	change := forgepool.RegistryChange{Kind: forgepool.RegistryRotateTreasury, Slot: 1, ActivationHeight: 102, Treasury: adminTestHash(900)}
	if err = writeJSON(filepath.Join(dir, "change.json"), change); err != nil {
		t.Fatal(err)
	}
	return dir, state, keys, work
}

func adminTestPrepare(t *testing.T, dir string, state rpc.ForgeRegistryReply) *approvalRequest {
	t.Helper()
	var out bytes.Buffer
	if err := run([]string{"prepare", "--state", filepath.Join(dir, "state.json"), "--change", filepath.Join(dir, "change.json"), "--chain-id", "7008914", "--genesis", state.Genesis.Hex(), "--out", filepath.Join(dir, "request.json")}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "treasuryScriptHash") || !strings.Contains(out.String(), state.Genesis.Hex()) {
		t.Fatal("review omitted important approval details")
	}
	var request approvalRequest
	if err := readJSON(filepath.Join(dir, "request.json"), &request); err != nil {
		t.Fatal(err)
	}
	return &request
}

func adminTestSign(t *testing.T, dir string, request *approvalRequest, key *ecdsa.PrivateKey, name string, accept bool) (string, string) {
	t.Helper()
	private := hex.EncodeToString(crypto.FromECDSA(key))
	path := filepath.Join(dir, name+".key")
	if err := os.WriteFile(path, []byte(private+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	digest := request.Digest
	if accept {
		digest = request.AcceptanceDigest
	}
	output := filepath.Join(dir, name+".signature.json")
	args := []string{"sign", "--request", filepath.Join(dir, "request.json"), "--key-file", path, "--approve-digest", digest.Hex(), "--out", output}
	if accept {
		args = append(args, "--accept")
	}
	var out bytes.Buffer
	if err := run(args, &out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), private) || bytes.Contains(data, []byte(private)) {
		t.Fatal("private key leaked into public output")
	}
	return output, private
}

func TestOfflinePrepareSignAssembleAndInspect(t *testing.T) {
	dir, state, keys, _ := adminTestFixture(t)
	request := adminTestPrepare(t, dir, state)
	if request.Change.Sequence != 1 || request.PreviewHeight != 100 {
		t.Fatal("prepare did not derive current sequence or inclusion preview")
	}
	first, _ := adminTestSign(t, dir, request, keys[0], "first", false)
	second, _ := adminTestSign(t, dir, request, keys[1], "second", false)
	var out bytes.Buffer
	if err := run([]string{"assemble", "--request", filepath.Join(dir, "request.json"), "--signature", second, "--signature", first, "--out", filepath.Join(dir, "operation.json")}, &out); err != nil {
		t.Fatal(err)
	}
	var operation forgepool.RegistryOperation
	if err := readJSON(filepath.Join(dir, "operation.json"), &operation); err != nil {
		t.Fatal(err)
	}
	if len(operation.Signatures) != 2 || operation.Change.Sequence != 1 || operation.ResultRoot == (common.Hash{}) {
		t.Fatal("assembled operation is incomplete")
	}
	out.Reset()
	if err := run([]string{"inspect", "--request", filepath.Join(dir, "request.json")}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), request.Digest.Hex()) || !strings.Contains(out.String(), "existing funds do not move") {
		t.Fatal("inspection omitted digest or treasury behavior")
	}
	if err := run([]string{"assemble", "--request", filepath.Join(dir, "request.json"), "--signature", first, "--signature", second, "--out", filepath.Join(dir, "operation.json")}, &out); err == nil {
		t.Fatal("overwrote an existing approved operation")
	}
}

func TestOfflineRejectsWrongNetworkAndMiningKeys(t *testing.T) {
	dir, state, _, work := adminTestFixture(t)
	args := []string{"prepare", "--state", filepath.Join(dir, "state.json"), "--change", filepath.Join(dir, "change.json"), "--chain-id", "70088", "--genesis", state.Genesis.Hex(), "--out", filepath.Join(dir, "wrong-request.json")}
	var out bytes.Buffer
	if err := run(args, &out); err == nil {
		t.Fatal("prepared a request for an unexpected network")
	}
	args[6] = "7008914"
	args[8] = adminTestHash(999999).Hex()
	if err := run(args, &out); err == nil {
		t.Fatal("prepared a request for an unexpected genesis")
	}
	request := adminTestPrepare(t, dir, state)
	private := hex.EncodeToString(crypto.FromECDSA(work[0]))
	keyFile := filepath.Join(dir, "mining.key")
	if err := os.WriteFile(keyFile, []byte(private), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"sign", "--request", filepath.Join(dir, "request.json"), "--key-file", keyFile, "--approve-digest", request.Digest.Hex(), "--out", filepath.Join(dir, "forbidden.signature.json")}, &out); err == nil {
		t.Fatal("mining key signed administrator operation")
	}
	if _, err := os.Stat(filepath.Join(dir, "forbidden.signature.json")); !os.IsNotExist(err) {
		t.Fatal("unauthorized key produced an output file")
	}
	if strings.Contains(out.String(), private) {
		t.Fatal("rejected private key appeared in output")
	}
}

func TestOfflineDigestReviewAndRequestTampering(t *testing.T) {
	dir, state, keys, _ := adminTestFixture(t)
	request := adminTestPrepare(t, dir, state)
	keyFile := filepath.Join(dir, "admin.key")
	if err := os.WriteFile(keyFile, []byte(hex.EncodeToString(crypto.FromECDSA(keys[0]))), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"sign", "--request", filepath.Join(dir, "request.json"), "--key-file", keyFile, "--approve-digest", adminTestHash(123).Hex(), "--out", filepath.Join(dir, "tampered.signature.json")}
	var out bytes.Buffer
	if err := run(args, &out); err == nil {
		t.Fatal("signed without exact digest review")
	}
	request.Change.Treasury = adminTestHash(901)
	data, _ := json.Marshal(request)
	if err := os.WriteFile(filepath.Join(dir, "request.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	args[6] = request.Digest.Hex()
	if err := run(args, &out); err == nil {
		t.Fatal("signed changed request using stale digest")
	}
}

func TestOfflineAdministratorRotationNeedsBothQuorums(t *testing.T) {
	dir, state, keys, _ := adminTestFixture(t)
	next := adminTestKeys(7001)
	change := forgepool.RegistryChange{Kind: forgepool.RegistryRotateAdmins, ActivationHeight: 104, Keys: adminTestAddresses(next), Quorum: 2}
	data, _ := json.Marshal(change)
	if err := os.WriteFile(filepath.Join(dir, "change.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	request := adminTestPrepare(t, dir, state)
	one, _ := adminTestSign(t, dir, request, keys[0], "old-one", false)
	two, _ := adminTestSign(t, dir, request, keys[1], "old-two", false)
	newOne, _ := adminTestSign(t, dir, request, next[0], "new-one", true)
	newTwo, _ := adminTestSign(t, dir, request, next[1], "new-two", true)
	var out bytes.Buffer
	base := []string{"assemble", "--request", filepath.Join(dir, "request.json"), "--out", filepath.Join(dir, "rotation.json"), "--signature", one, "--signature", two}
	if err := run(base, &out); err == nil {
		t.Fatal("rotation accepted without incoming possession quorum")
	}
	base = append(base, "--signature", newTwo, "--signature", newOne)
	if err := run(base, &out); err != nil {
		t.Fatal(err)
	}
}

func TestOfflineDuplicateAndForeignSignatureRejection(t *testing.T) {
	dir, state, keys, _ := adminTestFixture(t)
	request := adminTestPrepare(t, dir, state)
	one, _ := adminTestSign(t, dir, request, keys[0], "one", false)
	two, _ := adminTestSign(t, dir, request, keys[1], "two", false)
	var out bytes.Buffer
	args := []string{"assemble", "--request", filepath.Join(dir, "request.json"), "--out", filepath.Join(dir, "rejected.json"), "--signature", one, "--signature", one}
	if err := run(args, &out); err == nil {
		t.Fatal("duplicated one administrator to reach quorum")
	}
	var signature approvalSignature
	if err := readJSON(two, &signature); err != nil {
		t.Fatal(err)
	}
	signature.Genesis = adminTestHash(12)
	data, _ := json.Marshal(signature)
	if err := os.WriteFile(two, data, 0600); err != nil {
		t.Fatal(err)
	}
	args[len(args)-1] = two
	if err := run(args, &out); err == nil {
		t.Fatal("accepted a signature for another genesis")
	}
	if _, err := os.Stat(filepath.Join(dir, "rejected.json")); !os.IsNotExist(err) {
		t.Fatal("invalid signatures created approved operation output")
	}
}

func TestOfflineInputBoundsAndUnknownFields(t *testing.T) {
	dir, _, _, _ := adminTestFixture(t)
	for _, contents := range [][]byte{bytes.Repeat([]byte{'x'}, maxPublicFile+1), []byte(`{"version":1,"privateKey":"must-not-be-accepted"}`), []byte(`{} {}`)} {
		path := filepath.Join(dir, "invalid.json")
		if err := os.WriteFile(path, contents, 0600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := run([]string{"inspect", "--request", path}, &out); err == nil {
			t.Fatal("accepted unbounded or unsupported input")
		}
	}
}
