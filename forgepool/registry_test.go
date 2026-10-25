package forgepool

import (
	"bytes"
	"crypto/ecdsa"
	"math"
	"math/big"
	"sort"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

func registryTestKey(n int64) *ecdsa.PrivateKey {
	key, err := crypto.ToECDSA(common.LeftPadBytes(big.NewInt(n).Bytes(), 32))
	if err != nil {
		panic(err)
	}
	return key
}

func registryTestKeys(start int64) []*ecdsa.PrivateKey {
	keys := []*ecdsa.PrivateKey{registryTestKey(start), registryTestKey(start + 1), registryTestKey(start + 2)}
	sort.Slice(keys, func(i, j int) bool {
		a, b := crypto.PubkeyToAddress(keys[i].PublicKey), crypto.PubkeyToAddress(keys[j].PublicKey)
		return bytes.Compare(a[:], b[:]) < 0
	})
	return keys
}

func registryTestAddresses(keys []*ecdsa.PrivateKey) []common.Address {
	addresses := make([]common.Address, len(keys))
	for i, key := range keys {
		addresses[i] = crypto.PubkeyToAddress(key.PublicKey)
	}
	return addresses
}

func registryTestHash(n int64) common.Hash { return common.BigToHash(big.NewInt(n)) }

func registryTestFixture() (RegistrySnapshot, RegistryContext, []*ecdsa.PrivateKey) {
	keys := registryTestKeys(1)
	s := RegistrySnapshot{Admin: RegistryAdmin{Keys: registryTestAddresses(keys), Quorum: 2, Epoch: 1}, Coins: []RegistryCoin{
		{Slot: 1, ChainID: 0, Genesis: registryTestHash(100), Profile: RegistryProfileBitcoin80, Core: true, Treasury: registryTestHash(1)},
		{Slot: 2, ChainID: 98, Genesis: registryTestHash(101), Profile: RegistryProfileBitcoin80, Core: true, Treasury: registryTestHash(2)},
		{Slot: 3, ChainID: SOQAuxChainID, Genesis: registryTestHash(102), Profile: RegistryProfileSOQ, Core: true, Treasury: registryTestHash(3)},
	}}
	c := RegistryContext{ChainID: big.NewInt(7008914), Genesis: registryTestHash(7008914), WorkSigners: registryTestAddresses(registryTestKeys(21)), MinDelay: 2, MinOperationInterval: 1}
	return s, c, keys
}

func registryTestEnvelope(t testing.TB, s RegistrySnapshot, slots ...uint8) *RegistryEnvelope {
	t.Helper()
	root, err := RegistrySnapshotRoot(s)
	if err != nil {
		t.Fatal(err)
	}
	e := &RegistryEnvelope{Root: root, Checkpoint: s.Admin.LastOperationHeight}
	for _, slot := range slots {
		member, err := RegistryMembershipFor(s, slot)
		if err != nil {
			t.Fatal(err)
		}
		e.Members = append(e.Members, member)
	}
	sort.Slice(e.Members, func(i, j int) bool { return e.Members[i].Coin.ChainID < e.Members[j].Coin.ChainID })
	return e
}

func registryTestSign(t testing.TB, digest common.Hash, keys []*ecdsa.PrivateKey, quorum uint64) [][]byte {
	t.Helper()
	var signatures [][]byte
	for i := uint64(0); i < quorum; i++ {
		sig, err := crypto.Sign(digest[:], keys[i])
		if err != nil {
			t.Fatal(err)
		}
		signatures = append(signatures, sig)
	}
	return signatures
}

func registryTestOperation(t testing.TB, before RegistrySnapshot, c RegistryContext, height uint64, change RegistryChange, keys, nextKeys []*ecdsa.PrivateKey) (*RegistryEnvelope, RegistrySnapshot) {
	t.Helper()
	after, err := RegistryApplyChange(before, height, change, c)
	if err != nil {
		t.Fatal(err)
	}
	e := registryTestEnvelope(t, after, 1, 2, 3)
	parent, err := RegistrySnapshotRoot(before)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := RegistryOperationDigest(parent, height, change, c)
	if err != nil {
		t.Fatal(err)
	}
	quorum := before.Admin.Quorum
	if before.Admin.NextHeight != 0 && height >= before.Admin.NextHeight {
		quorum = before.Admin.NextQuorum
	}
	op := RegistryOperation{Before: registryCopy(before), Change: change, ResultRoot: e.Root, Signatures: registryTestSign(t, digest, keys, quorum)}
	if change.Kind == RegistryRotateAdmins {
		digest, err = RegistryAcceptanceDigest(parent, height, change, c)
		if err != nil {
			t.Fatal(err)
		}
		op.Acceptance = registryTestSign(t, digest, nextKeys, change.Quorum)
	}
	e.Operations = []RegistryOperation{op}
	return e, after
}

func registryTestExtra(slot uint8, enabled uint64) RegistryCoin {
	residues := []uint32{3, 4, 5, 6, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	return RegistryCoin{Slot: slot, ChainID: 3200 + residues[int(slot)-4], Genesis: registryTestHash(200 + int64(slot)), Profile: RegistryProfileBitcoin80, EnabledFrom: enabled, Treasury: registryTestHash(1000 + int64(slot))}
}

func registryTestRotate(sequence uint64, slot uint8, activation uint64, treasury int64) RegistryChange {
	return RegistryChange{Kind: RegistryRotateTreasury, Sequence: sequence, Slot: slot, ActivationHeight: activation, Treasury: registryTestHash(treasury)}
}

func TestRegistryBootstrapMembershipAndCanonicalEncoding(t *testing.T) {
	s, c, _ := registryTestFixture()
	if err := ValidateRegistrySnapshot(s, c); err != nil {
		t.Fatal(err)
	}
	root, err := RegistrySnapshotRoot(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, slots := range [][]uint8{{1}, {2}, {3}, {1, 2}, {1, 3}, {2, 3}, {1, 2, 3}} {
		e := registryTestEnvelope(t, s, slots...)
		view, err := ValidateRegistryEnvelope(root, 100, e, c)
		if err != nil || len(view.Coins) != len(slots) {
			t.Fatalf("slots=%v view=%v error=%v", slots, view, err)
		}
		encoded, err := rlp.EncodeToBytes(e)
		if err != nil {
			t.Fatal(err)
		}
		var decoded RegistryEnvelope
		if err = rlp.DecodeBytes(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if _, err = ValidateRegistryEnvelope(root, 100, &decoded, c); err != nil {
			t.Fatal(err)
		}
		hash1, _ := RegistryEnvelopeHash(e)
		hash2, _ := RegistryEnvelopeHash(&decoded)
		if hash1 != hash2 {
			t.Fatal("RLP round trip changed registry certificate commitment")
		}
	}
	if _, err := RegistryMembershipFor(s, 4); err == nil {
		t.Fatal("empty slot has membership")
	}
	if _, err := RegistryTreasury(s.Coins[2], 100); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryRejectsMalformedMembershipAndEnvelope(t *testing.T) {
	s, c, _ := registryTestFixture()
	root, _ := RegistrySnapshotRoot(s)
	tests := []struct {
		name   string
		mutate func(*RegistryEnvelope)
	}{
		{"wrong root", func(e *RegistryEnvelope) { e.Root = registryTestHash(999) }},
		{"wrong treasury", func(e *RegistryEnvelope) { e.Members[0].Coin.Treasury = registryTestHash(999) }},
		{"wrong sibling", func(e *RegistryEnvelope) { e.Members[0].Branch[2] = registryTestHash(999) }},
		{"short branch", func(e *RegistryEnvelope) { e.Members[0].Branch = e.Members[0].Branch[:4] }},
		{"long branch", func(e *RegistryEnvelope) { e.Members[0].Branch = append(e.Members[0].Branch, common.Hash{}) }},
		{"empty members", func(e *RegistryEnvelope) { e.Members = nil }},
		{"duplicate member", func(e *RegistryEnvelope) { e.Members = append(e.Members, e.Members[0]) }},
		{"unsorted members", func(e *RegistryEnvelope) { e.Members[0], e.Members[1] = e.Members[1], e.Members[0] }},
		{"too many members", func(e *RegistryEnvelope) {
			for len(e.Members) <= RegistryMaxMembers {
				e.Members = append(e.Members, e.Members[0])
			}
		}},
		{"checkpoint forgery", func(e *RegistryEnvelope) { e.Checkpoint = 1 }},
		{"multiple operations", func(e *RegistryEnvelope) { e.Operations = make([]RegistryOperation, 2) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := registryTestEnvelope(t, s, 1, 2, 3)
			test.mutate(e)
			if _, err := ValidateRegistryEnvelope(root, 100, e, c); err == nil {
				t.Fatal("accepted malformed registry envelope")
			}
		})
	}
	if _, err := ValidateRegistryEnvelope(root, 100, nil, c); err == nil {
		t.Fatal("accepted nil envelope")
	}
	if _, err := ValidateRegistryEnvelope(common.Hash{}, 100, registryTestEnvelope(t, s, 1), c); err == nil {
		t.Fatal("accepted absent parent root")
	}
	if _, err := ValidateRegistryEnvelope(root, 0, registryTestEnvelope(t, s, 1), c); err == nil {
		t.Fatal("accepted zero height")
	}
	c.ParentCheckpoint = 100
	if _, err := ValidateRegistryEnvelope(root, 100, registryTestEnvelope(t, s, 1), c); err == nil {
		t.Fatal("accepted nonancestor checkpoint")
	}
}

func TestRegistryTreasuryRotationAndFutureSelection(t *testing.T) {
	s, c, keys := registryTestFixture()
	root, _ := RegistrySnapshotRoot(s)
	change := registryTestRotate(1, 1, 102, 900)
	e, after := registryTestOperation(t, s, c, 100, change, keys, nil)
	if _, err := ValidateRegistryEnvelope(root, 100, e, c); err != nil {
		t.Fatal(err)
	}
	if s.Admin.Sequence != 0 || s.Coins[0].NextTreasuryHeight != 0 {
		t.Fatal("operation mutated parent snapshot")
	}
	for _, height := range []uint64{100, 101, 102, 103} {
		got, err := RegistryTreasury(after.Coins[0], height)
		want := s.Coins[0].Treasury
		if height >= 102 {
			want = change.Treasury
		}
		if err != nil || got != want {
			t.Fatalf("height %d treasury=%v want=%v error=%v", height, got, want, err)
		}
	}
	c.ParentCheckpoint = 100
	if _, err := ValidateRegistryEnvelope(e.Root, 101, registryTestEnvelope(t, after, 1, 2, 3), c); err != nil {
		t.Fatal(err)
	}
	if _, err := RegistryApplyChange(after, 101, registryTestRotate(2, 1, 103, 901), c); err == nil {
		t.Fatal("overwrote pending treasury before activation")
	}
	second, err := RegistryApplyChange(after, 102, registryTestRotate(2, 1, 104, 901), c)
	if err != nil {
		t.Fatal(err)
	}
	if second.Coins[0].Treasury != change.Treasury || second.Coins[0].NextTreasury != registryTestHash(901) {
		t.Fatal("did not preserve active rotated treasury")
	}
	for _, slot := range []uint8{1, 2, 3} {
		if _, err := RegistryApplyChange(s, 100, registryTestRotate(1, slot, 102, 900+int64(slot)), c); err != nil {
			t.Fatalf("core slot %d rotation failed: %v", slot, err)
		}
	}
}

func TestRegistryOperationReplayAuthorizationAndBounds(t *testing.T) {
	s, c, keys := registryTestFixture()
	root, _ := RegistrySnapshotRoot(s)
	change := registryTestRotate(1, 2, 105, 900)
	makeValid := func() *RegistryEnvelope { e, _ := registryTestOperation(t, s, c, 100, change, keys, nil); return e }
	tests := []struct {
		name   string
		mutate func(*RegistryEnvelope)
	}{
		{"wrong parent snapshot", func(e *RegistryEnvelope) { e.Operations[0].Before.Coins[0].Treasury = registryTestHash(999) }},
		{"wrong result root", func(e *RegistryEnvelope) { e.Operations[0].ResultRoot = registryTestHash(999) }},
		{"wrong operation checkpoint", func(e *RegistryEnvelope) { e.Checkpoint = 99 }},
		{"wrong before checkpoint", func(e *RegistryEnvelope) { e.Operations[0].Before.Admin.LastOperationHeight = 1 }},
		{"missing approval", func(e *RegistryEnvelope) { e.Operations[0].Signatures = nil }},
		{"too many approvals", func(e *RegistryEnvelope) {
			e.Operations[0].Signatures = append(e.Operations[0].Signatures, e.Operations[0].Signatures[0])
		}},
		{"duplicate approval", func(e *RegistryEnvelope) { e.Operations[0].Signatures[1] = e.Operations[0].Signatures[0] }},
		{"unsorted approval", func(e *RegistryEnvelope) {
			e.Operations[0].Signatures[0], e.Operations[0].Signatures[1] = e.Operations[0].Signatures[1], e.Operations[0].Signatures[0]
		}},
		{"malformed approval", func(e *RegistryEnvelope) { e.Operations[0].Signatures[0] = make([]byte, 65) }},
		{"short approval", func(e *RegistryEnvelope) { e.Operations[0].Signatures[0] = make([]byte, 64) }},
		{"unexpected possession", func(e *RegistryEnvelope) { e.Operations[0].Acceptance = e.Operations[0].Signatures }},
		{"altered request", func(e *RegistryEnvelope) { e.Operations[0].Change.Treasury = registryTestHash(901) }},
		{"ignored noncanonical fields", func(e *RegistryEnvelope) { e.Operations[0].Change.Quorum = 1 }},
		{"nonsequential request", func(e *RegistryEnvelope) { e.Operations[0].Change.Sequence = 2 }},
		{"oversized snapshot", func(e *RegistryEnvelope) { e.Operations[0].Before.Coins = make([]RegistryCoin, RegistryMaxCoins+1) }},
		{"oversized admin set", func(e *RegistryEnvelope) {
			e.Operations[0].Before.Admin.Keys = make([]common.Address, RegistryMaxAdminKeys+1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := makeValid()
			test.mutate(e)
			if _, err := ValidateRegistryEnvelope(root, 100, e, c); err == nil {
				t.Fatal("accepted unauthorized or malformed operation")
			}
		})
	}
	e := makeValid()
	digest, _ := RegistryOperationDigest(root, 100, change, c)
	e.Operations[0].Signatures = registryTestSign(t, digest, registryTestKeys(21), 2)
	if _, err := ValidateRegistryEnvelope(root, 100, e, c); err == nil {
		t.Fatal("mining work keys approved administrator operation")
	}
	for _, foreign := range []RegistryContext{func() RegistryContext { x := c; x.ChainID = big.NewInt(1); return x }(), func() RegistryContext { x := c; x.Genesis = registryTestHash(7008915); return x }()} {
		if _, err := ValidateRegistryEnvelope(root, 100, makeValid(), foreign); err == nil {
			t.Fatal("approval replayed across network identity")
		}
	}
	// Offline approvals survive a missed inclusion block while the parent root
	// remains current; only the checkpoint and deterministic result root change.
	after, err := RegistryApplyChange(s, 101, change, c)
	if err != nil {
		t.Fatal(err)
	}
	later := registryTestEnvelope(t, after, 1, 2, 3)
	later.Operations = makeValid().Operations
	later.Operations[0].ResultRoot = later.Root
	if _, err := ValidateRegistryEnvelope(root, 101, later, c); err != nil {
		t.Fatalf("offline approval tied to exact inclusion height: %v", err)
	}
	digestLater, _ := RegistryOperationDigest(root, 101, change, c)
	if digest != digestLater {
		t.Fatal("inclusion height changed administrator digest")
	}
	if _, err := ValidateRegistryEnvelope(root, 104, later, c); err == nil {
		t.Fatal("expired inclusion deadline accepted")
	}
	if _, err := ValidateRegistryEnvelope(later.Root, 102, later, c); err == nil {
		t.Fatal("operation replayed after state transition")
	}
}

func TestRegistryAdministratorRotationAndPossession(t *testing.T) {
	s, c, keys := registryTestFixture()
	root, _ := RegistrySnapshotRoot(s)
	nextKeys := registryTestKeys(11)
	change := RegistryChange{Kind: RegistryRotateAdmins, Sequence: 1, ActivationHeight: 104, Keys: registryTestAddresses(nextKeys), Quorum: 2}
	e, after := registryTestOperation(t, s, c, 100, change, keys, nextKeys)
	if _, err := ValidateRegistryEnvelope(root, 100, e, c); err != nil {
		t.Fatal(err)
	}
	missing := *e
	missing.Operations = append([]RegistryOperation(nil), e.Operations...)
	missing.Operations[0].Acceptance = nil
	if _, err := ValidateRegistryEnvelope(root, 100, &missing, c); err == nil {
		t.Fatal("rotated administrator keys without possession proof")
	}
	wrongDomain := *e
	wrongDomain.Operations = append([]RegistryOperation(nil), e.Operations...)
	wrongDomain.Operations[0].Acceptance = e.Operations[0].Signatures
	if _, err := ValidateRegistryEnvelope(root, 100, &wrongDomain, c); err == nil {
		t.Fatal("mining/approval signature reused as new-administrator acceptance")
	}
	if s.Admin.NextHeight != 0 || s.Admin.Keys[0] != registryTestAddresses(keys)[0] {
		t.Fatal("rotation mutated caller authority")
	}
	c.ParentCheckpoint = 100
	pendingChange := change
	pendingChange.Sequence = 2
	pendingChange.ActivationHeight = 106
	if _, err := RegistryApplyChange(after, 101, pendingChange, c); err == nil {
		t.Fatal("pending administrator rotation overwritten")
	}
	beforeActive, _ := registryTestOperation(t, after, c, 103, registryTestRotate(2, 1, 106, 900), keys, nil)
	if _, err := ValidateRegistryEnvelope(e.Root, 103, beforeActive, c); err != nil {
		t.Fatal(err)
	}
	afterActive, promoted := registryTestOperation(t, after, c, 104, registryTestRotate(2, 1, 106, 900), nextKeys, nil)
	if _, err := ValidateRegistryEnvelope(e.Root, 104, afterActive, c); err != nil {
		t.Fatal(err)
	}
	if promoted.Admin.Epoch != 2 || promoted.Admin.NextHeight != 0 || !registryKeysEqual(promoted.Admin.Keys, change.Keys) {
		t.Fatal("effective administrator authority did not rotate")
	}
	digest, _ := RegistryOperationDigest(e.Root, 104, afterActive.Operations[0].Change, c)
	afterActive.Operations[0].Signatures = registryTestSign(t, digest, keys, 2)
	if _, err := ValidateRegistryEnvelope(e.Root, 104, afterActive, c); err == nil {
		t.Fatal("retired administrator keys still authorized after effective block")
	}
	for _, overlap := range []RegistryChange{
		{Kind: RegistryRotateAdmins, Sequence: 1, ActivationHeight: 104, Keys: c.WorkSigners, Quorum: 2},
	} {
		if _, err := RegistryApplyChange(s, 100, overlap, c); err == nil {
			t.Fatal("work signer became pending administrator")
		}
	}
	badContext := c
	badContext.WorkSigners = append([]common.Address(nil), c.WorkSigners...)
	badContext.WorkSigners[0] = s.Admin.Keys[0]
	if err := ValidateRegistrySnapshot(s, badContext); err == nil {
		t.Fatal("active administrator overlaps work signer")
	}
	badContext.WorkSigners[0] = after.Admin.NextKeys[0]
	if err := ValidateRegistrySnapshot(after, badContext); err == nil {
		t.Fatal("pending administrator overlaps work signer")
	}
}

func TestRegistryCompatibleRegistrationDisableAndSlotReuse(t *testing.T) {
	s, c, keys := registryTestFixture()
	root, _ := RegistrySnapshotRoot(s)
	coin := registryTestExtra(4, 102)
	change := RegistryChange{Kind: RegistryRegister, Sequence: 1, ActivationHeight: 102, Coin: coin}
	e, registered := registryTestOperation(t, s, c, 100, change, keys, nil)
	if _, err := ValidateRegistryEnvelope(root, 100, e, c); err != nil {
		t.Fatal(err)
	}
	c.ParentCheckpoint = 100
	if _, err := ValidateRegistryEnvelope(e.Root, 101, registryTestEnvelope(t, registered, 1, 4), c); err == nil {
		t.Fatal("extra mined before activation")
	}
	if _, err := ValidateRegistryEnvelope(e.Root, 102, registryTestEnvelope(t, registered, 1, 4), c); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRegistryEnvelope(e.Root, 102, registryTestEnvelope(t, registered, 4), c); err == nil {
		t.Fatal("optional extra replaced core issuance source")
	}
	disable := RegistryChange{Kind: RegistryDisable, Sequence: 2, Slot: 4, ActivationHeight: 104}
	disabledEnvelope, disabled := registryTestOperation(t, registered, c, 102, disable, keys, nil)
	if _, err := ValidateRegistryEnvelope(e.Root, 102, disabledEnvelope, c); err != nil {
		t.Fatal(err)
	}
	c.ParentCheckpoint = 102
	if _, err := ValidateRegistryEnvelope(disabledEnvelope.Root, 103, registryTestEnvelope(t, disabled, 1, 4), c); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRegistryEnvelope(disabledEnvelope.Root, 104, registryTestEnvelope(t, disabled, 1, 4), c); err == nil {
		t.Fatal("disabled optional coin remains mineable")
	}
	if _, err := ValidateRegistryEnvelope(disabledEnvelope.Root, 104, registryTestEnvelope(t, disabled, 1, 2, 3), c); err != nil {
		t.Fatal("optional disable broke established sources:", err)
	}
	coin = registryTestExtra(4, 106)
	coin.ChainID += 32
	coin.Genesis = registryTestHash(555)
	replace := RegistryChange{Kind: RegistryRegister, Sequence: 3, ActivationHeight: 106, Coin: coin}
	if _, err := RegistryApplyChange(disabled, 103, replace, c); err == nil {
		t.Fatal("replaced optional coin before disable became effective")
	}
	replacement, again := registryTestOperation(t, disabled, c, 104, replace, keys, nil)
	if _, err := ValidateRegistryEnvelope(disabledEnvelope.Root, 104, replacement, c); err != nil {
		t.Fatal(err)
	}
	if len(again.Coins) != 4 || again.Coins[3].Genesis != coin.Genesis {
		t.Fatal("disabled slot not reused with approved replacement")
	}
	if _, err := ValidateRegistryEnvelope(disabledEnvelope.Root, 104, registryTestEnvelope(t, registered, 1), c); err == nil {
		t.Fatal("stale root accepted")
	}
}

func TestRegistryRejectsIdentityProfileAndTreeCollisions(t *testing.T) {
	s, c, _ := registryTestFixture()
	tests := []struct {
		name   string
		mutate func(*RegistrySnapshot)
	}{
		{"missing core", func(s *RegistrySnapshot) { s.Coins = s.Coins[:2] }},
		{"changed DGB identity", func(s *RegistrySnapshot) { s.Coins[0].ChainID = 1 }},
		{"changed DOGE profile", func(s *RegistrySnapshot) { s.Coins[1].Profile = RegistryProfileSOQ }},
		{"disabled core", func(s *RegistrySnapshot) { s.Coins[2].DisabledAt = 100 }},
		{"core flag removed", func(s *RegistrySnapshot) { s.Coins[1].Core = false }},
		{"missing native genesis", func(s *RegistrySnapshot) { s.Coins[2].Genesis = common.Hash{} }},
		{"missing treasury", func(s *RegistrySnapshot) { s.Coins[2].Treasury = common.Hash{} }},
		{"invalid pending treasury", func(s *RegistrySnapshot) { s.Coins[2].NextTreasuryHeight = 100 }},
		{"unsorted slots", func(s *RegistrySnapshot) { s.Coins[0], s.Coins[1] = s.Coins[1], s.Coins[0] }},
		{"duplicated slot", func(s *RegistrySnapshot) { s.Coins = append(s.Coins, s.Coins[2]) }},
		{"reserved RGN identity", func(s *RegistrySnapshot) {
			coin := registryTestExtra(4, 0)
			coin.ChainID = 20295
			s.Coins = append(s.Coins, coin)
		}},
		{"RGN tree collision", func(s *RegistrySnapshot) {
			coin := registryTestExtra(4, 0)
			coin.ChainID = 20295 + 32
			s.Coins = append(s.Coins, coin)
		}},
		{"DOGE tree collision", func(s *RegistrySnapshot) {
			coin := registryTestExtra(4, 0)
			coin.ChainID = 98 + 32
			s.Coins = append(s.Coins, coin)
		}},
		{"SOQ tree collision", func(s *RegistrySnapshot) {
			coin := registryTestExtra(4, 0)
			coin.ChainID = SOQAuxChainID + 32
			s.Coins = append(s.Coins, coin)
		}},
		{"oversized native identity", func(s *RegistrySnapshot) {
			coin := registryTestExtra(4, 0)
			coin.ChainID = 65536
			s.Coins = append(s.Coins, coin)
		}},
		{"unknown native profile", func(s *RegistrySnapshot) {
			coin := registryTestExtra(4, 0)
			coin.Profile = 99
			s.Coins = append(s.Coins, coin)
		}},
		{"extra grants core role", func(s *RegistrySnapshot) {
			coin := registryTestExtra(4, 0)
			coin.Core = true
			s.Coins = append(s.Coins, coin)
		}},
		{"duplicate native identity", func(s *RegistrySnapshot) {
			coin := registryTestExtra(4, 0)
			s.Coins = append(s.Coins, coin)
			coin.Slot = 5
			s.Coins = append(s.Coins, coin)
		}},
		{"extra tree collision", func(s *RegistrySnapshot) {
			coin := registryTestExtra(4, 0)
			s.Coins = append(s.Coins, coin)
			coin.Slot = 5
			coin.ChainID += 32
			s.Coins = append(s.Coins, coin)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bad := registryCopy(s)
			test.mutate(&bad)
			if err := ValidateRegistrySnapshot(bad, c); err == nil {
				t.Fatal("invalid registry snapshot accepted")
			}
			if _, err := RegistrySnapshotRoot(bad); err == nil {
				t.Fatal("invalid registry snapshot hashed")
			}
		})
	}
	for slot := uint8(1); slot <= 3; slot++ {
		if _, err := RegistryApplyChange(s, 100, RegistryChange{Kind: RegistryDisable, Sequence: 1, Slot: slot, ActivationHeight: 102}, c); err == nil {
			t.Fatal("disabled core through approved request")
		}
		coin := s.Coins[slot-1]
		coin.EnabledFrom = 102
		if _, err := RegistryApplyChange(s, 100, RegistryChange{Kind: RegistryRegister, Sequence: 1, ActivationHeight: 102, Coin: coin}, c); err == nil {
			t.Fatal("replaced core through approved request")
		}
	}
}

func TestRegistryTimelockRateLimitAndArithmetic(t *testing.T) {
	s, c, _ := registryTestFixture()
	change := registryTestRotate(1, 1, 102, 900)
	if _, err := RegistryApplyChange(s, 101, change, c); err == nil {
		t.Fatal("shortened treasury delay")
	}
	if _, err := RegistryApplyChange(s, math.MaxUint64-1, change, c); err == nil {
		t.Fatal("activation arithmetic overflow")
	}
	if _, err := RegistryApplyChange(s, 0, change, c); err == nil {
		t.Fatal("zero inclusion height")
	}
	change.Sequence = 0
	if _, err := RegistryApplyChange(s, 100, change, c); err == nil {
		t.Fatal("zero operation sequence")
	}
	change.Sequence = 1
	after, err := RegistryApplyChange(s, 100, change, c)
	if err != nil {
		t.Fatal(err)
	}
	c.MinOperationInterval = 4
	if _, err := RegistryApplyChange(after, 103, registryTestRotate(2, 2, 106, 901), c); err == nil {
		t.Fatal("operation rate limit bypass")
	}
	if _, err := RegistryApplyChange(after, 104, registryTestRotate(2, 2, 106, 901), c); err != nil {
		t.Fatal(err)
	}
	max := registryCopy(s)
	max.Admin.Sequence = math.MaxUint64
	max.Admin.LastOperationHeight = 1
	if _, err := RegistryApplyChange(max, 100, change, c); err == nil {
		t.Fatal("operation sequence overflow")
	}
	for _, bad := range []RegistryContext{
		func() RegistryContext { x := c; x.MinDelay = 0; return x }(),
		func() RegistryContext { x := c; x.MinOperationInterval = 0; return x }(),
		func() RegistryContext { x := c; x.Genesis = common.Hash{}; return x }(),
		func() RegistryContext { x := c; x.ChainID = nil; return x }(),
		func() RegistryContext { x := c; x.WorkSigners = nil; return x }(),
	} {
		if err := ValidateRegistrySnapshot(s, bad); err == nil {
			t.Fatal("accepted invalid immutable context")
		}
	}
}

func TestRegistryReorgBranchesAreParentIndependent(t *testing.T) {
	s, c, keys := registryTestFixture()
	root, _ := RegistrySnapshotRoot(s)
	a, sa := registryTestOperation(t, s, c, 100, registryTestRotate(1, 1, 102, 900), keys, nil)
	b, sb := registryTestOperation(t, s, c, 100, registryTestRotate(1, 1, 102, 901), keys, nil)
	for _, e := range []*RegistryEnvelope{a, b} {
		if _, err := ValidateRegistryEnvelope(root, 100, e, c); err != nil {
			t.Fatal(err)
		}
	}
	if a.Root == b.Root {
		t.Fatal("conflicting branch registry states have same root")
	}
	c.ParentCheckpoint = 100
	if _, err := ValidateRegistryEnvelope(a.Root, 101, registryTestEnvelope(t, sb, 1), c); err == nil {
		t.Fatal("branch B snapshot accepted on branch A")
	}
	if _, err := ValidateRegistryEnvelope(b.Root, 101, registryTestEnvelope(t, sa, 1), c); err == nil {
		t.Fatal("branch A snapshot accepted on branch B")
	}
	childA, _ := registryTestOperation(t, sa, c, 101, registryTestRotate(2, 2, 104, 902), keys, nil)
	if _, err := ValidateRegistryEnvelope(a.Root, 101, childA, c); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRegistryEnvelope(b.Root, 101, childA, c); err == nil {
		t.Fatal("administrator operation replayed across conflicting parent roots")
	}
	// Fresh reconstruction from the same headers requires no cached canonical
	// registry or native RPC and yields identical roots and treasury selection.
	fresh, err := RegistryApplyChange(s, 100, a.Operations[0].Change, c)
	if err != nil {
		t.Fatal(err)
	}
	freshRoot, _ := RegistrySnapshotRoot(fresh)
	if freshRoot != a.Root {
		t.Fatal("fresh header replay differs from prior transition")
	}
}

func TestRegistryMaximumSnapshotAndEnvelopeSize(t *testing.T) {
	s, c, keys := registryTestFixture()
	for slot := uint8(4); slot <= RegistryMaxCoins; slot++ {
		s.Coins = append(s.Coins, registryTestExtra(slot, 0))
	}
	if err := ValidateRegistrySnapshot(s, c); err != nil {
		t.Fatal(err)
	}
	root, _ := RegistrySnapshotRoot(s)
	e := registryTestEnvelope(t, s, 1, 2, 3, 4, 5, 6)
	if _, err := ValidateRegistryEnvelope(root, 100, e, c); err != nil {
		t.Fatal(err)
	}
	b, _ := rlp.EncodeToBytes(e)
	if len(b) > 2048 {
		t.Fatalf("ordinary six-source registry witness unexpectedly large: %d", len(b))
	}
	op, _ := registryTestOperation(t, s, c, 100, registryTestRotate(1, 1, 102, 900), keys, nil)
	b, _ = rlp.EncodeToBytes(op)
	if len(b) > 4096 {
		t.Fatalf("maximum snapshot administrator envelope unexpectedly large: %d", len(b))
	}
	t.Logf("ordinary three-source=%d bytes six-source=%d bytes full-16-coin operation=%d bytes", func() int { x, _ := rlp.EncodeToBytes(registryTestEnvelope(t, s, 1, 2, 3)); return len(x) }(), func() int { x, _ := rlp.EncodeToBytes(e); return len(x) }(), len(b))
}

func BenchmarkRegistryThreeSourceMembership(b *testing.B) {
	s, c, _ := registryTestFixture()
	root, _ := RegistrySnapshotRoot(s)
	e := registryTestEnvelope(b, s, 1, 2, 3)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ValidateRegistryEnvelope(root, 100, e, c); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRegistrySixSourceMembership(b *testing.B) {
	s, c, _ := registryTestFixture()
	for slot := uint8(4); slot <= RegistryMaxCoins; slot++ {
		s.Coins = append(s.Coins, registryTestExtra(slot, 0))
	}
	root, _ := RegistrySnapshotRoot(s)
	e := registryTestEnvelope(b, s, 1, 2, 3, 4, 5, 6)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ValidateRegistryEnvelope(root, 100, e, c); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRegistryFullSnapshotOperation(b *testing.B) {
	s, c, keys := registryTestFixture()
	for slot := uint8(4); slot <= RegistryMaxCoins; slot++ {
		s.Coins = append(s.Coins, registryTestExtra(slot, 0))
	}
	root, _ := RegistrySnapshotRoot(s)
	e, _ := registryTestOperation(b, s, c, 100, registryTestRotate(1, 1, 102, 900), keys, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ValidateRegistryEnvelope(root, 100, e, c); err != nil {
			b.Fatal(err)
		}
	}
}
