package forgepool

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	RegistryMaxCoins               = 16
	RegistryMaxMembers             = 6
	RegistryMaxAdminKeys           = 5
	RegistryTreeDepth              = 5
	RegistryProfileBitcoin80 uint8 = 1
	RegistryProfileSOQ       uint8 = 2
	RegistryRegister         uint8 = 1
	RegistryRotateTreasury   uint8 = 2
	RegistryDisable          uint8 = 3
	RegistryRotateAdmins     uint8 = 4
)

// RegistryContext contains immutable verification policy, never runtime RPC data.
// Genesis identifies the RGN network, not an external native chain.
type RegistryContext struct {
	ChainID              *big.Int
	Genesis              common.Hash
	WorkSigners          []common.Address
	MinDelay             uint64
	MinOperationInterval uint64
	// ParentCheckpoint is the previous header's last administrator-operation
	// height. It is authenticated by that header, not a local cache setting.
	ParentCheckpoint uint64
}

type RegistryAdmin struct {
	Keys                []common.Address
	Quorum              uint64
	Epoch               uint64
	Sequence            uint64
	LastOperationHeight uint64
	NextKeys            []common.Address
	NextQuorum          uint64
	NextEpoch           uint64
	NextHeight          uint64
}

// Core identities occupy slots 1 (DGB), 2 (DOGE), and 3 (SOQ). They cannot
// expire, be removed, change identity, or stop being issuance anchors.
type RegistryCoin struct {
	Slot               uint8
	ChainID            uint32
	Genesis            common.Hash
	Profile            uint8
	Core               bool
	EnabledFrom        uint64
	DisabledAt         uint64
	Treasury           common.Hash
	NextTreasury       common.Hash
	NextTreasuryHeight uint64
}

// Snapshots occur only in administrator-operation blocks. Their size is bounded
// by RegistryMaxCoins; ordinary blocks prove only the entries they actually use.
type RegistrySnapshot struct {
	Admin RegistryAdmin
	Coins []RegistryCoin
}

type RegistryMembership struct {
	Coin   RegistryCoin
	Branch []common.Hash
}

// Every field irrelevant to Kind must remain zero. This makes signed requests
// canonical and avoids authorization bytes with ignored alternate meanings.
type RegistryChange struct {
	Kind             uint8
	Sequence         uint64
	Slot             uint8
	Coin             RegistryCoin
	Treasury         common.Hash
	ActivationHeight uint64
	Keys             []common.Address
	Quorum           uint64
}

type RegistryOperation struct {
	Before     RegistrySnapshot
	Change     RegistryChange
	Signatures [][]byte
	// New administrators prove possession on a distinct acceptance domain.
	Acceptance [][]byte
	ResultRoot common.Hash
}

type RegistryEnvelope struct {
	Root       common.Hash
	Checkpoint uint64
	Members    []RegistryMembership
	Operations []RegistryOperation
}

type RegistryView struct {
	Root  common.Hash
	Coins []RegistryCoin
}

func registryKeysValid(keys []common.Address, quorum uint64) error {
	if len(keys) == 0 || len(keys) > RegistryMaxAdminKeys || quorum == 0 || quorum > uint64(len(keys)) {
		return errors.New("invalid bounded registry administrator quorum")
	}
	for i, key := range keys {
		if key == (common.Address{}) || (i > 0 && bytes.Compare(keys[i-1][:], key[:]) >= 0) {
			return errors.New("registry administrator keys are not nonzero and uniquely sorted")
		}
	}
	return nil
}

func registryAdminValid(a RegistryAdmin) error {
	if err := registryKeysValid(a.Keys, a.Quorum); err != nil {
		return err
	}
	if a.Epoch == 0 || (a.Sequence == 0 && a.LastOperationHeight != 0) || (a.Sequence != 0 && a.LastOperationHeight == 0) {
		return errors.New("invalid registry administrator sequence or epoch")
	}
	if a.NextHeight == 0 {
		if len(a.NextKeys) != 0 || a.NextQuorum != 0 || a.NextEpoch != 0 {
			return errors.New("inactive administrator rotation carries pending fields")
		}
	} else {
		if a.Epoch == math.MaxUint64 || a.NextEpoch != a.Epoch+1 || a.NextHeight <= a.LastOperationHeight {
			return errors.New("invalid pending registry administrator epoch")
		}
		if err := registryKeysValid(a.NextKeys, a.NextQuorum); err != nil {
			return err
		}
		if registryKeysEqual(a.Keys, a.NextKeys) && a.Quorum == a.NextQuorum {
			return errors.New("administrator rotation does not change its authority")
		}
	}
	return nil
}

func registryKeysEqual(a, b []common.Address) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func registryCoinValid(c RegistryCoin) error {
	if c.Slot == 0 || c.Slot > RegistryMaxCoins || c.Genesis == (common.Hash{}) || c.Treasury == (common.Hash{}) {
		return errors.New("invalid registry coin slot, identity, or treasury")
	}
	if c.DisabledAt != 0 && c.DisabledAt <= c.EnabledFrom {
		return errors.New("invalid registry coin disable height")
	}
	if (c.NextTreasuryHeight == 0) != (c.NextTreasury == (common.Hash{})) || (c.NextTreasuryHeight != 0 && (c.NextTreasuryHeight <= c.EnabledFrom || c.NextTreasury == c.Treasury)) {
		return errors.New("invalid pending registry treasury")
	}
	if c.Slot <= 3 {
		ids := []uint32{0, 98, SOQAuxChainID}
		profile := RegistryProfileBitcoin80
		if c.Slot == 3 {
			profile = RegistryProfileSOQ
		}
		if !c.Core || c.ChainID != ids[int(c.Slot)-1] || c.Profile != profile || c.DisabledAt != 0 {
			return errors.New("immutable registry core identity or profile changed")
		}
	} else if c.Core || c.Profile != RegistryProfileBitcoin80 || c.ChainID == 0 || c.ChainID > 65535 || c.ChainID == 98 || c.ChainID == SOQAuxChainID || c.ChainID == 20295 {
		return errors.New("unsupported or reserved auxiliary registry identity/profile")
	}
	return nil
}

func registrySnapshotValid(s RegistrySnapshot) error {
	if len(s.Coins) < 3 || len(s.Coins) > RegistryMaxCoins {
		return errors.New("invalid bounded registry coin count")
	}
	if err := registryAdminValid(s.Admin); err != nil {
		return err
	}
	seen := make(map[uint32]bool, len(s.Coins))
	// The installed canonical AuxPoW tree has at most 32 leaves. Equal native
	// chain-ID residues cannot be separated by any AuxPoW nonce at that size.
	// Reject registrations that could make the ordinary all-source job invalid.
	indices := map[uint32]bool{20295 % 32: true}
	for i, coin := range s.Coins {
		if err := registryCoinValid(coin); err != nil {
			return err
		}
		if (i < 3 && coin.Slot != uint8(i+1)) || (i > 0 && s.Coins[i-1].Slot >= coin.Slot) || seen[coin.ChainID] {
			return errors.New("registry slots or chain identities are duplicate, missing, or unsorted")
		}
		seen[coin.ChainID] = true
		if coin.ChainID != 0 {
			index := coin.ChainID % 32
			if indices[index] {
				return errors.New("registry auxiliary identity collides in the installed 32-leaf tree")
			}
			indices[index] = true
		}
	}
	return nil
}

func registryContextValid(c RegistryContext) error {
	if c.ChainID == nil || c.ChainID.Sign() <= 0 || c.ChainID.BitLen() > 256 || c.Genesis == (common.Hash{}) || c.MinDelay == 0 || c.MinOperationInterval == 0 || len(c.WorkSigners) == 0 || len(c.WorkSigners) > MaxTemplateSigners {
		return errors.New("invalid registry verification context")
	}
	seen := make(map[common.Address]bool, len(c.WorkSigners))
	for _, a := range c.WorkSigners {
		if a == (common.Address{}) || seen[a] {
			return errors.New("invalid registry work signer context")
		}
		seen[a] = true
	}
	return nil
}

func registryDisjoint(a RegistryAdmin, c RegistryContext) error {
	for _, work := range c.WorkSigners {
		for _, admin := range a.Keys {
			if work == admin {
				return errors.New("registry administrator is also a mining work signer")
			}
		}
		for _, admin := range a.NextKeys {
			if work == admin {
				return errors.New("pending registry administrator is also a mining work signer")
			}
		}
	}
	return nil
}

func ValidateRegistrySnapshot(s RegistrySnapshot, c RegistryContext) error {
	if err := registryContextValid(c); err != nil {
		return err
	}
	if err := registrySnapshotValid(s); err != nil {
		return err
	}
	return registryDisjoint(s.Admin, c)
}

func registryEmpty(slot uint8) common.Hash {
	return crypto.Keccak256Hash([]byte("RGN-FORGE-REGISTRY-EMPTY-V1"), []byte{slot})
}
func registryNode(left, right common.Hash) common.Hash {
	return crypto.Keccak256Hash([]byte("RGN-FORGE-REGISTRY-NODE-V1"), left[:], right[:])
}
func registryLeaf(slot uint8, value interface{}) (common.Hash, error) {
	b, err := rlp.EncodeToBytes(value)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash([]byte("RGN-FORGE-REGISTRY-LEAF-V1"), []byte{slot}, b), nil
}

func registryLeaves(s RegistrySnapshot) ([]common.Hash, error) {
	if err := registrySnapshotValid(s); err != nil {
		return nil, err
	}
	leaves := make([]common.Hash, 1<<RegistryTreeDepth)
	for i := range leaves {
		leaves[i] = registryEmpty(uint8(i))
	}
	var err error
	leaves[0], err = registryLeaf(0, s.Admin)
	if err != nil {
		return nil, err
	}
	for _, coin := range s.Coins {
		leaves[coin.Slot], err = registryLeaf(coin.Slot, coin)
		if err != nil {
			return nil, err
		}
	}
	return leaves, nil
}

func RegistrySnapshotRoot(s RegistrySnapshot) (common.Hash, error) {
	level, err := registryLeaves(s)
	if err != nil {
		return common.Hash{}, err
	}
	for len(level) > 1 {
		next := make([]common.Hash, len(level)/2)
		for i := range next {
			next[i] = registryNode(level[2*i], level[2*i+1])
		}
		level = next
	}
	return level[0], nil
}

func RegistryMembershipFor(s RegistrySnapshot, slot uint8) (RegistryMembership, error) {
	level, err := registryLeaves(s)
	if err != nil {
		return RegistryMembership{}, err
	}
	var out RegistryMembership
	found := false
	for _, coin := range s.Coins {
		if coin.Slot == slot {
			out.Coin, found = coin, true
			break
		}
	}
	if !found {
		return out, errors.New("registry membership requested for empty slot")
	}
	index := int(slot)
	for len(level) > 1 {
		out.Branch = append(out.Branch, level[index^1])
		next := make([]common.Hash, len(level)/2)
		for i := range next {
			next[i] = registryNode(level[2*i], level[2*i+1])
		}
		level = next
		index >>= 1
	}
	return out, nil
}

func registryMembershipRoot(m RegistryMembership) (common.Hash, error) {
	if len(m.Branch) != RegistryTreeDepth {
		return common.Hash{}, errors.New("invalid registry membership branch length")
	}
	if err := registryCoinValid(m.Coin); err != nil {
		return common.Hash{}, err
	}
	h, err := registryLeaf(m.Coin.Slot, m.Coin)
	if err != nil {
		return common.Hash{}, err
	}
	index := m.Coin.Slot
	for _, sibling := range m.Branch {
		if index&1 == 0 {
			h = registryNode(h, sibling)
		} else {
			h = registryNode(sibling, h)
		}
		index >>= 1
	}
	return h, nil
}

func RegistryTreasury(coin RegistryCoin, height uint64) (common.Hash, error) {
	if err := registryCoinValid(coin); err != nil {
		return common.Hash{}, err
	}
	if height < coin.EnabledFrom || (coin.DisabledAt != 0 && height >= coin.DisabledAt) {
		return common.Hash{}, errors.New("registry coin is not active at this block")
	}
	if coin.NextTreasuryHeight != 0 && height >= coin.NextTreasuryHeight {
		return coin.NextTreasury, nil
	}
	return coin.Treasury, nil
}

func registryChangeCanonical(ch RegistryChange) error {
	if ch.Sequence == 0 || ch.ActivationHeight == 0 {
		return errors.New("missing registry operation sequence or effective height")
	}
	zeroCoin := RegistryCoin{}
	switch ch.Kind {
	case RegistryRegister:
		if ch.Slot != 0 || ch.Coin.Slot <= 3 || ch.Coin.EnabledFrom != ch.ActivationHeight || ch.Coin.DisabledAt != 0 || ch.Coin.NextTreasury != (common.Hash{}) || ch.Coin.NextTreasuryHeight != 0 || ch.Treasury != (common.Hash{}) || len(ch.Keys) != 0 || ch.Quorum != 0 {
			return errors.New("noncanonical coin registration request")
		}
		return registryCoinValid(ch.Coin)
	case RegistryRotateTreasury:
		if ch.Slot == 0 || ch.Slot > RegistryMaxCoins || ch.Coin != zeroCoin || ch.Treasury == (common.Hash{}) || len(ch.Keys) != 0 || ch.Quorum != 0 {
			return errors.New("noncanonical treasury rotation request")
		}
	case RegistryDisable:
		if ch.Slot <= 3 || ch.Slot > RegistryMaxCoins || ch.Coin != zeroCoin || ch.Treasury != (common.Hash{}) || len(ch.Keys) != 0 || ch.Quorum != 0 {
			return errors.New("noncanonical optional coin disable request")
		}
	case RegistryRotateAdmins:
		if ch.Slot != 0 || ch.Coin != zeroCoin || ch.Treasury != (common.Hash{}) {
			return errors.New("noncanonical administrator rotation request")
		}
		return registryKeysValid(ch.Keys, ch.Quorum)
	default:
		return errors.New("unsupported registry operation")
	}
	return nil
}

func registryOperationDigest(parentRoot common.Hash, height uint64, ch RegistryChange, c RegistryContext, acceptance bool) (common.Hash, error) {
	if err := registryContextValid(c); err != nil {
		return common.Hash{}, err
	}
	if parentRoot == (common.Hash{}) || height == 0 {
		return common.Hash{}, errors.New("missing registry operation ancestry")
	}
	if err := registryChangeCanonical(ch); err != nil {
		return common.Hash{}, err
	}
	domain := "RGN-FORGE-REGISTRY-ADMIN-OP-V1"
	if acceptance {
		domain = "RGN-FORGE-REGISTRY-ADMIN-ACCEPT-V1"
	}
	// Inclusion height is deliberately not signed. A request remains usable
	// while its parent registry state is current and its activation deadline
	// still satisfies MinDelay. The resulting checkpoint uses actual height.
	b, err := rlp.EncodeToBytes(struct {
		Domain     string
		ChainID    *big.Int
		Genesis    common.Hash
		ParentRoot common.Hash
		Change     RegistryChange
	}{domain, c.ChainID, c.Genesis, parentRoot, ch})
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(b), nil
}

func RegistryOperationDigest(parentRoot common.Hash, height uint64, ch RegistryChange, c RegistryContext) (common.Hash, error) {
	return registryOperationDigest(parentRoot, height, ch, c, false)
}
func RegistryAcceptanceDigest(parentRoot common.Hash, height uint64, ch RegistryChange, c RegistryContext) (common.Hash, error) {
	return registryOperationDigest(parentRoot, height, ch, c, true)
}

// RegistryEnvelopeHash binds all membership, approval, and checkpoint bytes to
// a V5 work certificate. It performs cheap dimension checks before encoding.
func RegistryEnvelopeHash(envelope *RegistryEnvelope) (common.Hash, error) {
	if err := registryEnvelopeDimensions(envelope); err != nil {
		return common.Hash{}, err
	}
	b, err := rlp.EncodeToBytes(envelope)
	if err != nil {
		return common.Hash{}, err
	}
	if len(b) > MaxOGGProofSize {
		return common.Hash{}, errors.New("registry envelope exceeds proof byte limit")
	}
	return crypto.Keccak256Hash([]byte("RGN-FORGE-REGISTRY-ENVELOPE-V1"), b), nil
}

func registryEnvelopeDimensions(envelope *RegistryEnvelope) error {
	if envelope == nil || envelope.Root == (common.Hash{}) || len(envelope.Operations) > 1 || len(envelope.Members) == 0 || len(envelope.Members) > RegistryMaxMembers {
		return errors.New("invalid bounded registry envelope")
	}
	for _, member := range envelope.Members {
		if len(member.Branch) != RegistryTreeDepth {
			return errors.New("invalid registry membership branch length")
		}
	}
	for _, op := range envelope.Operations {
		if len(op.Before.Coins) > RegistryMaxCoins || len(op.Before.Admin.Keys) > RegistryMaxAdminKeys || len(op.Before.Admin.NextKeys) > RegistryMaxAdminKeys || len(op.Change.Keys) > RegistryMaxAdminKeys || len(op.Signatures) > RegistryMaxAdminKeys || len(op.Acceptance) > RegistryMaxAdminKeys {
			return errors.New("registry operation exceeds bounded capacity")
		}
		for _, list := range [][][]byte{op.Signatures, op.Acceptance} {
			for _, sig := range list {
				if len(sig) != 65 {
					return errors.New("invalid registry signature size")
				}
			}
		}
	}
	return nil
}

func registryCopy(s RegistrySnapshot) RegistrySnapshot {
	out := s
	out.Admin.Keys = append([]common.Address(nil), s.Admin.Keys...)
	out.Admin.NextKeys = append([]common.Address(nil), s.Admin.NextKeys...)
	out.Coins = append([]RegistryCoin(nil), s.Coins...)
	return out
}

func registryPromote(s *RegistrySnapshot, height uint64) {
	a := &s.Admin
	if a.NextHeight != 0 && height >= a.NextHeight {
		a.Keys, a.Quorum, a.Epoch = a.NextKeys, a.NextQuorum, a.NextEpoch
		a.NextKeys = nil
		a.NextQuorum, a.NextEpoch, a.NextHeight = 0, 0, 0
	}
	for i := range s.Coins {
		coin := &s.Coins[i]
		if coin.NextTreasuryHeight != 0 && height >= coin.NextTreasuryHeight {
			coin.Treasury = coin.NextTreasury
			coin.NextTreasury = common.Hash{}
			coin.NextTreasuryHeight = 0
		}
	}
}

// RegistryApplyChange checks and computes an authorized operation's requested
// state transition. Signatures are checked separately by ValidateRegistryEnvelope.
// It never mutates the supplied snapshot or its key slices.
func RegistryApplyChange(before RegistrySnapshot, height uint64, ch RegistryChange, c RegistryContext) (RegistrySnapshot, error) {
	if err := ValidateRegistrySnapshot(before, c); err != nil {
		return RegistrySnapshot{}, err
	}
	if err := registryChangeCanonical(ch); err != nil {
		return RegistrySnapshot{}, err
	}
	if height == 0 || height > math.MaxUint64-c.MinDelay || ch.ActivationHeight < height+c.MinDelay {
		return RegistrySnapshot{}, errors.New("registry operation does not satisfy the future activation delay")
	}
	if before.Admin.Sequence == math.MaxUint64 || ch.Sequence != before.Admin.Sequence+1 {
		return RegistrySnapshot{}, errors.New("registry operation sequence does not follow parent state")
	}
	if before.Admin.LastOperationHeight != 0 && (height < before.Admin.LastOperationHeight || height-before.Admin.LastOperationHeight < c.MinOperationInterval) {
		return RegistrySnapshot{}, errors.New("registry operation rate limit")
	}
	out := registryCopy(before)
	registryPromote(&out, height)
	index := -1
	for i, coin := range out.Coins {
		if coin.Slot == ch.Slot {
			index = i
			break
		}
	}
	switch ch.Kind {
	case RegistryRegister:
		for i, coin := range out.Coins {
			if coin.Slot == ch.Coin.Slot {
				index = i
				break
			}
		}
		if index >= 0 {
			old := out.Coins[index]
			if old.Core || old.DisabledAt == 0 || height < old.DisabledAt {
				return RegistrySnapshot{}, errors.New("registry registration cannot replace an active or pending coin")
			}
			out.Coins[index] = ch.Coin
		} else {
			if len(out.Coins) >= RegistryMaxCoins {
				return RegistrySnapshot{}, errors.New("registry coin capacity reached")
			}
			at := 0
			for at < len(out.Coins) && out.Coins[at].Slot < ch.Coin.Slot {
				at++
			}
			out.Coins = append(out.Coins, RegistryCoin{})
			copy(out.Coins[at+1:], out.Coins[at:])
			out.Coins[at] = ch.Coin
		}
	case RegistryRotateTreasury:
		if index < 0 {
			return RegistrySnapshot{}, errors.New("treasury rotation targets an empty registry slot")
		}
		coin := &out.Coins[index]
		if coin.DisabledAt != 0 || coin.NextTreasuryHeight != 0 || coin.Treasury == ch.Treasury || ch.ActivationHeight <= coin.EnabledFrom {
			return RegistrySnapshot{}, errors.New("treasury rotation would overwrite pending, inactive, or unchanged treasury state")
		}
		coin.NextTreasury, coin.NextTreasuryHeight = ch.Treasury, ch.ActivationHeight
	case RegistryDisable:
		if index < 0 {
			return RegistrySnapshot{}, errors.New("disable targets an empty registry slot")
		}
		coin := &out.Coins[index]
		if coin.Core || coin.DisabledAt != 0 || coin.NextTreasuryHeight != 0 || ch.ActivationHeight <= coin.EnabledFrom {
			return RegistrySnapshot{}, errors.New("optional coin disable overwrites protected or pending state")
		}
		coin.DisabledAt = ch.ActivationHeight
	case RegistryRotateAdmins:
		if out.Admin.NextHeight != 0 || out.Admin.Epoch == math.MaxUint64 || (registryKeysEqual(out.Admin.Keys, ch.Keys) && out.Admin.Quorum == ch.Quorum) {
			return RegistrySnapshot{}, errors.New("administrator rotation overwrites pending or unchanged authority")
		}
		out.Admin.NextKeys = append([]common.Address(nil), ch.Keys...)
		out.Admin.NextQuorum, out.Admin.NextEpoch, out.Admin.NextHeight = ch.Quorum, out.Admin.Epoch+1, ch.ActivationHeight
	}
	out.Admin.Sequence, out.Admin.LastOperationHeight = ch.Sequence, height
	if err := ValidateRegistrySnapshot(out, c); err != nil {
		return RegistrySnapshot{}, err
	}
	return out, nil
}

func registrySignatures(digest common.Hash, sigs [][]byte, keys []common.Address, quorum uint64) error {
	if err := registryKeysValid(keys, quorum); err != nil {
		return err
	}
	if uint64(len(sigs)) != quorum {
		return errors.New("registry approval requires exactly the configured quorum")
	}
	allowed := make(map[common.Address]bool, len(keys))
	for _, a := range keys {
		allowed[a] = true
	}
	var previous common.Address
	for i, sig := range sigs {
		if len(sig) != 65 || !crypto.ValidateSignatureValues(sig[64], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:64]), true) {
			return errors.New("invalid registry administrator signature")
		}
		pub, err := crypto.SigToPub(digest[:], sig)
		if err != nil {
			return errors.New("invalid registry administrator recovery")
		}
		address := crypto.PubkeyToAddress(*pub)
		if !allowed[address] || (i > 0 && bytes.Compare(previous[:], address[:]) >= 0) {
			return errors.New("unauthorized, duplicate, or unsorted registry administrator signature")
		}
		previous = address
	}
	return nil
}

// ValidateRegistryEnvelope is header-only and must run even when a caller skips
// expensive seal verification. A parent registry root is its sole history input.
// The caller must first enforce the existing 16 KiB envelope byte limit.
func ValidateRegistryEnvelope(parentRoot common.Hash, height uint64, envelope *RegistryEnvelope, c RegistryContext) (*RegistryView, error) {
	if err := registryContextValid(c); err != nil {
		return nil, err
	}
	if err := registryEnvelopeDimensions(envelope); err != nil {
		return nil, err
	}
	if parentRoot == (common.Hash{}) || height == 0 || c.ParentCheckpoint >= height {
		return nil, errors.New("invalid registry ancestry or checkpoint")
	}
	if len(envelope.Operations) == 0 {
		if envelope.Root != parentRoot {
			return nil, errors.New("registry root changed without an administrator operation")
		}
		if envelope.Checkpoint != c.ParentCheckpoint {
			return nil, errors.New("registry checkpoint changed without an administrator operation")
		}
	} else {
		op := &envelope.Operations[0]
		if op.Before.Admin.LastOperationHeight != c.ParentCheckpoint || envelope.Checkpoint != height {
			return nil, errors.New("registry operation checkpoint differs from ancestry")
		}
		// Bound attacker-controlled slices before hashing or signature recovery.
		if len(op.Before.Coins) > RegistryMaxCoins || len(op.Before.Admin.Keys) > RegistryMaxAdminKeys || len(op.Before.Admin.NextKeys) > RegistryMaxAdminKeys || len(op.Change.Keys) > RegistryMaxAdminKeys || len(op.Signatures) > RegistryMaxAdminKeys || len(op.Acceptance) > RegistryMaxAdminKeys {
			return nil, errors.New("registry operation exceeds bounded capacity")
		}
		if err := ValidateRegistrySnapshot(op.Before, c); err != nil {
			return nil, err
		}
		beforeRoot, err := RegistrySnapshotRoot(op.Before)
		if err != nil || beforeRoot != parentRoot {
			return nil, errors.New("registry operation snapshot differs from parent commitment")
		}
		after, err := RegistryApplyChange(op.Before, height, op.Change, c)
		if err != nil {
			return nil, err
		}
		digest, err := RegistryOperationDigest(parentRoot, height, op.Change, c)
		if err != nil {
			return nil, err
		}
		authority := registryCopy(op.Before)
		registryPromote(&authority, height)
		if err := registrySignatures(digest, op.Signatures, authority.Admin.Keys, authority.Admin.Quorum); err != nil {
			return nil, err
		}
		if op.Change.Kind == RegistryRotateAdmins {
			digest, err := RegistryAcceptanceDigest(parentRoot, height, op.Change, c)
			if err != nil {
				return nil, err
			}
			if err := registrySignatures(digest, op.Acceptance, op.Change.Keys, op.Change.Quorum); err != nil {
				return nil, fmt.Errorf("new registry administrator possession: %v", err)
			}
		} else if len(op.Acceptance) != 0 {
			return nil, errors.New("unexpected administrator acceptance signatures")
		}
		root, err := RegistrySnapshotRoot(after)
		if err != nil || root != op.ResultRoot || root != envelope.Root {
			return nil, errors.New("registry transition result root mismatch")
		}
	}
	view := &RegistryView{Root: envelope.Root, Coins: make([]RegistryCoin, 0, len(envelope.Members))}
	anchor := false
	for i, member := range envelope.Members {
		if i > 0 && envelope.Members[i-1].Coin.ChainID >= member.Coin.ChainID {
			return nil, errors.New("registry members are not uniquely sorted by chain identity")
		}
		root, err := registryMembershipRoot(member)
		if err != nil {
			return nil, err
		}
		if root != envelope.Root {
			return nil, errors.New("registry membership differs from committed registry root")
		}
		if _, err := RegistryTreasury(member.Coin, height); err != nil {
			return nil, err
		}
		anchor = anchor || member.Coin.Core
		view.Coins = append(view.Coins, member.Coin)
	}
	if !anchor {
		return nil, errors.New("registry mining work requires a DGB, DOGE, or SOQ core source")
	}
	return view, nil
}
