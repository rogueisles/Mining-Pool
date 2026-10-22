package forgepool

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	OGGProofVersion    = uint8(1)
	OGGTemplateVersion = uint8(1)

	// V2 keeps the existing proof envelope and ExternalTemplate structure.
	// The low two bits declare which external chains participate.
	//
	// 0x20 = reserved / INVALID (OGG-only mining is forbidden)
	// 0x21 = OGG + DGB
	// 0x22 = OGG + DOGE
	// 0x23 = OGG + DGB + DOGE
	OGGTemplateVersionV2Base = uint8(0x20)
	ExternalDGB              = uint8(0x01)
	ExternalDOGE             = uint8(0x02)
	ExternalMask             = ExternalDGB | ExternalDOGE

	MaxOGGProofSize  = 16 * 1024
	MaxOGGMinerExtra = 32
)

var oggProofMagic = []byte{'O', 'G', 'G', 'F'}

type ExternalTemplate struct {
	Version     uint8
	DGBPrevHash common.Hash
	DGBBits     uint32
	DogeAuxHash common.Hash
	ValidFrom   uint64
	Expires     uint64
}

type OGGAuxPowProofLegacy struct {
	MinerExtra         []byte
	ParentHeader       []byte
	ParentCoinbase     []byte
	ParentMerkleBranch []common.Hash
	ParentMerkleIndex  uint32
	DogeHeader         []byte
	DogeCoinbase       []byte
	DogeMerkleBranch   []common.Hash
	DogeMerkleIndex    uint32
	Template           ExternalTemplate
	Signatures         [][]byte
}

type OGGAuxPowProofV2 struct {
	MinerExtra         []byte
	ParentHeader       []byte
	ParentCoinbase     []byte
	ParentMerkleBranch []common.Hash
	ParentMerkleIndex  uint32
	DogeHeader         []byte
	DogeCoinbase       []byte
	DogeMerkleBranch   []common.Hash
	DogeMerkleIndex    uint32
	Template           ExternalTemplate
	Signatures         [][]byte
	OGGChainBranch     []common.Hash
	DOGEChainBranch    []common.Hash
}

type OGGAuxPowProof struct {
	Multi              *MultiProof
	MinerExtra         []byte
	ParentHeader       []byte
	ParentCoinbase     []byte
	ParentMerkleBranch []common.Hash
	ParentMerkleIndex  uint32
	DogeHeader         []byte
	DogeCoinbase       []byte
	DogeMerkleBranch   []common.Hash
	DogeMerkleIndex    uint32
	Template           ExternalTemplate
	Signatures         [][]byte
	OGGChainBranch     []common.Hash
	DOGEChainBranch    []common.Hash
}

type templateSigningPayload struct {
	Domain   []byte
	Template ExternalTemplate
}

type templateWorkSigningPayload struct {
	Domain   []byte
	Template ExternalTemplate
	WorkHash common.Hash
}

func TemplateDomain(t ExternalTemplate) string {
	if t.Version&^ExternalMask == OGGTemplateVersionV2Base {
		return "OGG-FORGE-TEMPLATE-V2-WORK"
	}
	return "OGG-FORGE-TEMPLATE-V1"
}

// TemplateDigest preserves the legacy V1 digest.
//
// V2 authorization MUST use TemplateSigningDigest so the signature is bound to
// the exact issued OGG WorkHash.
func TemplateDigest(t ExternalTemplate) common.Hash {
	blob, _ := rlp.EncodeToBytes(templateSigningPayload{
		Domain:   []byte(TemplateDomain(t)),
		Template: t,
	})
	return crypto.Keccak256Hash(blob)
}

// TemplateSigningDigest returns the consensus signing digest.
//
// V1 remains byte-for-byte compatible and carries no separate WorkHash.
// V2 requires a non-zero exact OGG WorkHash and includes it in the signed RLP.
func TemplateSigningDigest(t ExternalTemplate, workHash common.Hash) (common.Hash, error) {
	if t.Version == OGGTemplateVersion {
		if workHash != (common.Hash{}) {
			return common.Hash{}, errors.New("V1 template must not carry V2 WorkHash binding")
		}
		return TemplateDigest(t), nil
	}

	if _, err := TemplateExternalMask(t); err != nil {
		return common.Hash{}, err
	}
	if workHash == (common.Hash{}) {
		return common.Hash{}, errors.New("V2 template requires exact OGG WorkHash")
	}

	blob, _ := rlp.EncodeToBytes(templateWorkSigningPayload{
		Domain:   []byte(TemplateDomain(t)),
		Template: t,
		WorkHash: workHash,
	})
	return crypto.Keccak256Hash(blob), nil
}

// TemplateExternalMask validates the participation declaration and returns the
// active external-chain mask.
//
// V1 remains strictly DGB+DOGE. V2 requires Digibyte, Dogecoin, or both.
// OGG-only mining is forbidden and must fail closed.
func TemplateExternalMask(t ExternalTemplate) (uint8, error) {
	if t.Version == OGGTemplateVersion {
		if t.DGBPrevHash == (common.Hash{}) || t.DGBBits == 0 ||
			t.DogeAuxHash == (common.Hash{}) {
			return 0, errors.New("incomplete FORGE V1 external template")
		}
		return ExternalMask, nil
	}

	if t.Version&^ExternalMask != OGGTemplateVersionV2Base {
		return 0, fmt.Errorf("unsupported external template version %#x", t.Version)
	}

	mask := t.Version & ExternalMask

	// Match OGG consensus exactly: every FORGE job must contribute to at
	// least one external revenue chain. No pool or signer may authorize
	// OGG-only work.
	if mask == 0 {
		return 0, errors.New("FORGE V2 requires at least one external revenue chain")
	}

	if mask&ExternalDGB != 0 {
		if t.DGBPrevHash == (common.Hash{}) || t.DGBBits == 0 {
			return 0, errors.New("V2 Digibyte participation missing signed fields")
		}
	} else if t.DGBPrevHash != (common.Hash{}) || t.DGBBits != 0 {
		return 0, errors.New("V2 inactive Digibyte has non-zero signed fields")
	}

	if mask&ExternalDOGE != 0 {
		if t.DogeAuxHash == (common.Hash{}) {
			return 0, errors.New("V2 Dogecoin participation missing signed hash")
		}
	} else if t.DogeAuxHash != (common.Hash{}) {
		return 0, errors.New("V2 inactive Dogecoin has non-zero signed hash")
	}

	return mask, nil
}

func EncodeOGGProof(p *OGGAuxPowProof) ([]byte, error) {
	if p != nil && p.Multi != nil {
		return EncodeMultiProof(p.Multi)
	}
	if p == nil {
		return nil, errors.New("nil OGG AuxPoW proof")
	}
	if len(p.MinerExtra) > MaxOGGMinerExtra {
		return nil, fmt.Errorf("OGG miner extra too long: %d", len(p.MinerExtra))
	}
	var wire interface{} = legacyOGGAuxPowProof(p)
	version := uint8(1)
	if len(p.OGGChainBranch) > 0 || len(p.DOGEChainBranch) > 0 {
		wire = asOGGAuxPowProofV2(p)
		version = 2
	}
	payload, err := rlp.EncodeToBytes(wire)
	if err != nil {
		return nil, err
	}
	out := append(append(append([]byte{}, oggProofMagic...), version), payload...)
	if len(out) > MaxOGGProofSize {
		return nil, fmt.Errorf("OGG AuxPoW proof too large: %d", len(out))
	}
	return out, nil
}

func DecodeOGGProof(blob []byte) (*OGGAuxPowProof, error) {
	if len(blob) < 5 || !bytes.Equal(blob[:4], oggProofMagic) {
		return nil, errors.New("OGGF proof envelope not present")
	}
	if len(blob) > MaxOGGProofSize {
		return nil, errors.New("OGGF proof envelope too large")
	}
	if blob[4] == 3 || blob[4] == 4 {
		var m MultiProof
		if err := rlp.DecodeBytes(blob[4+1:], &m); err != nil {
			return nil, err
		}
		if len(m.MinerExtra) > 32 {
			return nil, errors.New("multi miner extra too long")
		}
		if m.Template.Version != blob[4] {
			return nil, errors.New("multi proof envelope version mismatch")
		}
		return &OGGAuxPowProof{Multi: &m, MinerExtra: m.MinerExtra, ParentHeader: m.ParentHeader, ParentCoinbase: m.ParentCoinbase, ParentMerkleBranch: m.ParentMerkleBranch, Template: ExternalTemplate{ValidFrom: m.Template.ValidFrom, Expires: m.Template.Expires}}, nil
	}
	if blob[4] != 1 && blob[4] != 2 {
		return nil, fmt.Errorf("unsupported OGGF proof version %d", blob[4])
	}
	var p OGGAuxPowProof
	if blob[4] == 1 {
		var old OGGAuxPowProofLegacy
		if err := rlp.DecodeBytes(blob[5:], &old); err != nil {
			return nil, err
		}
		p = *upgradeOGGAuxPowProof(&old)
	} else {
		var v2 OGGAuxPowProofV2
		if err := rlp.DecodeBytes(blob[5:], &v2); err != nil {
			return nil, err
		}
		p = *fromOGGAuxPowProofV2(&v2)
	}

	if len(p.MinerExtra) > MaxOGGMinerExtra {
		return nil, errors.New("OGG miner extra too long")
	}
	return &p, nil
}

// ValidateTemplateSignatures mirrors node consensus while failing loudly for
// duplicate and unauthorized signers, which makes signer-service faults
// diagnosable before a block is submitted.
func ValidateTemplateSignatures(t ExternalTemplate, signatures [][]byte, authorized []common.Address, quorum uint64, at time.Time) error {
	if _, err := TemplateExternalMask(t); err != nil {
		return err
	}
	when := uint64(at.Unix())
	if t.Expires < t.ValidFrom || when < t.ValidFrom || when > t.Expires {
		return errors.New("external template expired or not yet valid")
	}
	if quorum == 0 || quorum > uint64(len(authorized)) {
		return errors.New("invalid signer quorum")
	}
	allowed := make(map[common.Address]struct{}, len(authorized))
	for _, a := range authorized {
		allowed[a] = struct{}{}
	}
	seen := make(map[common.Address]struct{})
	digest := TemplateDigest(t)
	for _, sig := range signatures {
		if len(sig) != 65 {
			return errors.New("signer returned non-65-byte signature")
		}
		pub, err := crypto.SigToPub(digest.Bytes(), sig)
		if err != nil {
			return fmt.Errorf("invalid template signature: %v", err)
		}
		addr := crypto.PubkeyToAddress(*pub)
		if _, ok := allowed[addr]; !ok {
			return fmt.Errorf("unauthorized template signer %s", addr.Hex())
		}
		if _, ok := seen[addr]; ok {
			return fmt.Errorf("duplicate template signer %s", addr.Hex())
		}
		seen[addr] = struct{}{}
	}
	if uint64(len(seen)) < quorum {
		return fmt.Errorf("insufficient signer quorum: have %d want %d", len(seen), quorum)
	}
	return nil
}

// ValidateTemplateSignaturesForWork verifies V2 signer quorum against the
// exact issued OGG WorkHash. A signature from another OGG work item is invalid.
func ValidateTemplateSignaturesForWork(t ExternalTemplate, workHash common.Hash, signatures [][]byte, authorized []common.Address, quorum uint64, at time.Time) error {
	if t.Version&^ExternalMask != OGGTemplateVersionV2Base {
		return errors.New("work-bound signature validation requires FORGE V2")
	}
	if _, err := TemplateExternalMask(t); err != nil {
		return err
	}

	when := uint64(at.Unix())
	if t.Expires < t.ValidFrom || when < t.ValidFrom || when > t.Expires {
		return errors.New("external template expired or not yet valid")
	}
	if quorum == 0 || quorum > uint64(len(authorized)) {
		return errors.New("invalid signer quorum")
	}

	digest, err := TemplateSigningDigest(t, workHash)
	if err != nil {
		return err
	}

	allowed := make(map[common.Address]struct{}, len(authorized))
	for _, a := range authorized {
		allowed[a] = struct{}{}
	}

	seen := make(map[common.Address]struct{})
	for _, sig := range signatures {
		if len(sig) != 65 {
			return errors.New("signer returned non-65-byte signature")
		}
		pub, err := crypto.SigToPub(digest.Bytes(), sig)
		if err != nil {
			return fmt.Errorf("invalid template signature: %v", err)
		}
		addr := crypto.PubkeyToAddress(*pub)
		if _, ok := allowed[addr]; !ok {
			return fmt.Errorf("unauthorized template signer %s", addr.Hex())
		}
		if _, ok := seen[addr]; ok {
			return fmt.Errorf("duplicate template signer %s", addr.Hex())
		}
		seen[addr] = struct{}{}
	}

	if uint64(len(seen)) < quorum {
		return fmt.Errorf("insufficient signer quorum: have %d want %d", len(seen), quorum)
	}
	return nil
}

func commonHashes(in []Hash) []common.Hash {
	out := make([]common.Hash, len(in))
	for i := range in {
		out[i] = common.BytesToHash(in[i][:])
	}
	return out
}

// BuildOGGProof assembles the exact consensus proof for V1 or V2.
//
// V1 requires DGB+DOGE exactly. V2 may carry DGB only, DOGE only, or both.
// OGG-only has already been made invalid by TemplateExternalMask.
func (j *DigibyteJob) BuildOGGProof(extra1, extra2 []byte, ntime, nonce uint32, minerExtra []byte, tmpl ExternalTemplate, signatures [][]byte) (*OGGAuxPowProof, error) {
	if j == nil {
		return nil, errors.New("missing FORGE parent job")
	}

	mask, err := TemplateExternalMask(tmpl)
	if err != nil {
		return nil, err
	}

	dgbActive := mask&ExternalDGB != 0
	dogeActive := mask&ExternalDOGE != 0

	if j.DigibyteActive != dgbActive {
		return nil, errors.New("FORGE parent Digibyte participation does not match signed template")
	}
	if (j.DOGE != nil) != dogeActive {
		return nil, errors.New("FORGE parent Dogecoin participation does not match signed template")
	}

	parentHeader, err := j.Header(extra1, extra2, ntime, nonce)
	if err != nil {
		return nil, err
	}
	parentCoinbase, err := j.Coinbase.BaseCoinbase(extra1, extra2)
	if err != nil {
		return nil, err
	}

	proof := &OGGAuxPowProof{
		MinerExtra:         append([]byte(nil), minerExtra...),
		ParentHeader:       parentHeader,
		ParentCoinbase:     parentCoinbase,
		ParentMerkleBranch: commonHashes(j.MerkleBranch),
		ParentMerkleIndex:  0,
		Template:           tmpl,
		Signatures:         signatures,
	}

	if dogeActive {
		proof.DogeHeader = append([]byte(nil), j.DOGE.Header...)
		proof.DogeCoinbase = append([]byte(nil), j.DOGE.Coinbase...)
		proof.DogeMerkleBranch = commonHashes(j.DOGE.MerkleBranch)
		proof.DogeMerkleIndex = 0
	}

	if j.AuxTree != nil {
		proof.OGGChainBranch = commonHashes(j.AuxTree.Branches[OGGAuxChainID])
		proof.DOGEChainBranch = commonHashes(j.AuxTree.Branches[DOGEAuxChainID])
	}
	return proof, nil
}

func legacyOGGAuxPowProof(p *OGGAuxPowProof) *OGGAuxPowProofLegacy {
	return &OGGAuxPowProofLegacy{MinerExtra: p.MinerExtra, ParentHeader: p.ParentHeader, ParentCoinbase: p.ParentCoinbase, ParentMerkleBranch: p.ParentMerkleBranch, ParentMerkleIndex: p.ParentMerkleIndex, DogeHeader: p.DogeHeader, DogeCoinbase: p.DogeCoinbase, DogeMerkleBranch: p.DogeMerkleBranch, DogeMerkleIndex: p.DogeMerkleIndex, Template: p.Template, Signatures: p.Signatures}
}
func upgradeOGGAuxPowProof(p *OGGAuxPowProofLegacy) *OGGAuxPowProof {
	return &OGGAuxPowProof{MinerExtra: p.MinerExtra, ParentHeader: p.ParentHeader, ParentCoinbase: p.ParentCoinbase, ParentMerkleBranch: p.ParentMerkleBranch, ParentMerkleIndex: p.ParentMerkleIndex, DogeHeader: p.DogeHeader, DogeCoinbase: p.DogeCoinbase, DogeMerkleBranch: p.DogeMerkleBranch, DogeMerkleIndex: p.DogeMerkleIndex, Template: p.Template, Signatures: p.Signatures}
}

func asOGGAuxPowProofV2(p *OGGAuxPowProof) *OGGAuxPowProofV2 {
	return &OGGAuxPowProofV2{MinerExtra: p.MinerExtra, ParentHeader: p.ParentHeader, ParentCoinbase: p.ParentCoinbase, ParentMerkleBranch: p.ParentMerkleBranch, ParentMerkleIndex: p.ParentMerkleIndex, DogeHeader: p.DogeHeader, DogeCoinbase: p.DogeCoinbase, DogeMerkleBranch: p.DogeMerkleBranch, DogeMerkleIndex: p.DogeMerkleIndex, Template: p.Template, Signatures: p.Signatures, OGGChainBranch: p.OGGChainBranch, DOGEChainBranch: p.DOGEChainBranch}
}
func fromOGGAuxPowProofV2(p *OGGAuxPowProofV2) *OGGAuxPowProof {
	return &OGGAuxPowProof{MinerExtra: p.MinerExtra, ParentHeader: p.ParentHeader, ParentCoinbase: p.ParentCoinbase, ParentMerkleBranch: p.ParentMerkleBranch, ParentMerkleIndex: p.ParentMerkleIndex, DogeHeader: p.DogeHeader, DogeCoinbase: p.DogeCoinbase, DogeMerkleBranch: p.DogeMerkleBranch, DogeMerkleIndex: p.DogeMerkleIndex, Template: p.Template, Signatures: p.Signatures, OGGChainBranch: p.OGGChainBranch, DOGEChainBranch: p.DOGEChainBranch}
}
