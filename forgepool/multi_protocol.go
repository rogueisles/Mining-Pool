package forgepool

import (
	"bytes"
	"encoding/hex"
	"errors"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"math/big"
	"sort"
)

const MultiExternalMask uint8 = 31
const MultiDomain = "OGG-FORGE-MULTI-TEMPLATE-V3-WORK"
const MultiDomainV4 = "RGN-FORGE-MULTI-TEMPLATE-V4-WORK"
const MaxTemplateSigners = 16

type MultiAuxRef struct {
	ChainID uint32
	Hash    common.Hash
}
type MultiTemplate struct {
	Version            uint8
	Mask               uint8
	DGBPrevHash        common.Hash
	DGBBits            uint32
	ParentMinTime      uint32
	ParentVersion      uint32
	ParentCoinbaseHash common.Hash
	ParentBranchHash   common.Hash
	ExtraOffset        uint32
	ExtraSize          uint32
	Aux                []MultiAuxRef
	ValidFrom          uint64
	Expires            uint64
}
type MultiAuxProof struct {
	ChainID      uint32
	Header       []byte
	Coinbase     []byte
	MerkleBranch []common.Hash
}
type MultiProof struct {
	MinerExtra         []byte
	ParentHeader       []byte
	ParentCoinbase     []byte
	ParentMerkleBranch []common.Hash
	Aux                []MultiAuxProof
	Template           MultiTemplate
	Signatures         [][]byte
}
type MultiEvidence struct {
	ChainID   uint32
	Candidate ParentTemplate
}
type MultiSignRequest struct {
	WorkHash common.Hash
	Template MultiTemplate
	AuxNonce uint32
	DGB      *ParentTemplate
	Aux      []MultiEvidence
}

func MultiChainBit(id uint32) uint8 {
	switch id {
	case 98:
		return 2
	case 63:
		return 4
	case 50:
		return 8
	case 16:
		return 16
	}
	return 0
}
func MultiTemplateValid(t MultiTemplate) error {
	if (t.Version != 3 && t.Version != 4) || t.Mask == 0 || t.Mask & ^MultiExternalMask != 0 || t.ValidFrom == 0 || t.Expires <= t.ValidFrom || t.Expires-t.ValidFrom > 300 {
		return errors.New("invalid multi-chain participation or validity")
	}
	if t.ParentCoinbaseHash == (common.Hash{}) || t.ParentBranchHash == (common.Hash{}) || t.ExtraSize < 1 || t.ExtraSize > 24 || t.ExtraOffset < 42 || t.ExtraOffset > 142 {
		return errors.New("missing parent body authorization")
	}
	if t.Mask&1 != 0 {
		if t.DGBPrevHash == (common.Hash{}) || t.DGBBits == 0 || t.ParentMinTime == 0 {
			return errors.New("missing native DGB parent")
		}
	} else if t.DGBPrevHash != (common.Hash{}) || t.DGBBits != 0 || t.ParentMinTime != 0 {
		return errors.New("inactive DGB carries signed parent fields")
	}
	mask := t.Mask & 1
	var prev uint32
	for _, a := range t.Aux {
		bit := MultiChainBit(a.ChainID)
		if bit == 0 || a.ChainID <= prev || a.Hash == (common.Hash{}) || mask&bit != 0 {
			return errors.New("invalid or duplicate auxiliary identity")
		}
		mask |= bit
		prev = a.ChainID
	}
	if mask != t.Mask {
		return errors.New("participation mask and auxiliary set differ")
	}
	return nil
}
func MultiDigest(t MultiTemplate, work common.Hash) (common.Hash, error) {
	if err := MultiTemplateValid(t); err != nil {
		return common.Hash{}, err
	}
	if work == (common.Hash{}) {
		return common.Hash{}, errors.New("zero OGG work")
	}
	domain := MultiDomain
	if t.Version == 4 {
		domain = MultiDomainV4
	}
	payload := struct {
		Domain   []byte
		Template MultiTemplate
		WorkHash common.Hash
	}{[]byte(domain), t, work}
	b, err := rlp.EncodeToBytes(payload)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(b), nil
}
func ValidateMultiSignatures(t MultiTemplate, work common.Hash, sigs [][]byte, allowed []common.Address, quorum uint64, stamp uint64) error {
	digest, err := MultiDigest(t, work)
	if err != nil {
		return err
	}
	if t.ValidFrom != stamp || quorum == 0 || quorum > uint64(len(allowed)) {
		return errors.New("invalid multi-chain work timestamp or quorum")
	}
	strict := t.Version == 4
	if strict && (len(allowed) > MaxTemplateSigners || uint64(len(sigs)) != quorum) {
		return errors.New("invalid bounded multi-chain certificate size")
	}
	authorized := map[common.Address]bool{}
	for _, a := range allowed {
		authorized[a] = true
	}
	seen := map[common.Address]bool{}
	var previous common.Address
	for _, sig := range sigs {
		if len(sig) != 65 || !crypto.ValidateSignatureValues(sig[64], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:64]), true) {
			if strict {
				return errors.New("invalid multi-chain certificate signature")
			}
			continue
		}
		pub, e := crypto.SigToPub(digest.Bytes(), sig)
		if e == nil {
			a := crypto.PubkeyToAddress(*pub)
			if authorized[a] {
				if strict && (seen[a] || (len(seen) != 0 && bytes.Compare(previous[:], a[:]) >= 0)) {
					return errors.New("multi-chain certificate signatures are not uniquely ordered")
				}
				seen[a] = true
				previous = a
			} else if strict {
				return errors.New("unauthorized multi-chain certificate signer")
			}
		} else if strict {
			return errors.New("invalid multi-chain certificate recovery")
		}
	}
	if uint64(len(seen)) < quorum {
		return errors.New("insufficient multi-chain template signatures")
	}
	return nil
}
func BuildMultiParent(tpl ParentTemplate, work string, aux map[uint32]*DogeCandidate, script []byte, nonce uint32, extra1, extra2 int, dgb bool) (*DigibyteJob, error) {
	leaf, err := ParseRawHashHex(work)
	if err != nil {
		return nil, err
	}
	if !dgb && len(aux) == 0 {
		return nil, errors.New("OGG-only mining forbidden")
	}
	leaves := map[uint32]Hash{OGGAuxChainID: leaf}
	other := map[uint32]*DogeCandidate{}
	var doge *DogeCandidate
	for id, c := range aux {
		if c == nil || MultiChainBit(id) == 0 || uint32(c.Version)>>16 != id {
			return nil, errors.New("unsupported native auxiliary candidate")
		}
		leaves[id] = c.Hash
		if id == 98 {
			doge = c
		} else {
			other[id] = c
		}
	}
	tree, err := NewAuxTree(leaves, nonce)
	if err != nil {
		return nil, err
	}
	plan, err := BuildCoinbasePlan(tpl.Height, tpl.CoinbaseAux, tree.Commitment(), script, tpl.CoinbaseValue, tpl.WitnessCommitment, extra1, extra2)
	if err != nil {
		return nil, err
	}
	return &DigibyteJob{Template: tpl, OGGLeaf: leaf, DOGE: doge, AuxNonce: nonce, AuxTree: tree, AdditionalAux: other, OGGIndex: tree.Indices[OGGAuxChainID], DOGEIndex: tree.Indices[DOGEAuxChainID], Commitment: tree.Commitment(), Coinbase: plan, MerkleBranch: CoinbaseMerkleBranch(tpl.Transactions), PayoutScript: append([]byte{}, script...), ExtraNonce1Size: extra1, ExtraNonce2Size: extra2, DigibyteActive: dgb}, nil
}
func (j *DigibyteJob) MultiTemplate(stamp, expires uint64) (MultiTemplate, error) {
	version := uint8(3)
	if j.ProtocolVersion == 4 {
		version = 4
	}
	t := MultiTemplate{Version: version, ValidFrom: stamp, Expires: expires}
	plan := j.Coinbase
	if version == 4 {
		var e error
		plan, e = certificatePlan(j.Coinbase, common.Hash{})
		if e != nil {
			return t, e
		}
	}
	cb, e := plan.BaseCoinbase(make([]byte, j.ExtraNonce1Size), make([]byte, j.ExtraNonce2Size))
	if e != nil {
		return t, e
	}
	t.ParentVersion = uint32(j.Template.Version)
	t.ParentCoinbaseHash = crypto.Keccak256Hash(cb)
	t.ExtraOffset = uint32(len(j.Coinbase.Coinb1))
	t.ExtraSize = uint32(j.ExtraNonce1Size + j.ExtraNonce2Size)
	branch := []common.Hash{}
	for _, h := range j.MerkleBranch {
		branch = append(branch, common.BytesToHash(h[:]))
	}
	raw, e := rlp.EncodeToBytes(branch)
	if e != nil {
		return t, e
	}
	t.ParentBranchHash = crypto.Keccak256Hash(raw)
	if j.DigibyteActive {
		t.Mask |= 1
		p := reverseHash(j.Template.Previous)
		t.DGBPrevHash = common.BytesToHash(p[:])
		t.DGBBits = j.Template.Bits
		t.ParentMinTime = j.Template.CurTime
	}
	candidates := map[uint32]*DogeCandidate{}
	for id, c := range j.AdditionalAux {
		candidates[id] = c
	}
	if j.DOGE != nil {
		candidates[98] = j.DOGE
	}
	ids := []int{}
	for id := range candidates {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	for _, id := range ids {
		c := candidates[uint32(id)]
		t.Mask |= MultiChainBit(uint32(id))
		t.Aux = append(t.Aux, MultiAuxRef{uint32(id), common.HexToHash(c.DisplayHash)})
	}
	return t, MultiTemplateValid(t)
}
func (j *DigibyteJob) BuildMultiProof(extra1, extra2 []byte, ntime, nonce uint32, t MultiTemplate, sigs [][]byte) (*MultiProof, error) {
	if t.Version == 4 {
		if e := j.CheckCertificate(t, sigs); e != nil {
			return nil, e
		}
	}
	header, err := j.Header(extra1, extra2, ntime, nonce)
	if err != nil {
		return nil, err
	}
	cb, err := j.Coinbase.BaseCoinbase(extra1, extra2)
	if err != nil {
		return nil, err
	}
	p := &MultiProof{ParentHeader: header, ParentCoinbase: cb, Template: t, Signatures: sigs}
	for _, h := range j.MerkleBranch {
		p.ParentMerkleBranch = append(p.ParentMerkleBranch, common.BytesToHash(h[:]))
	}
	for _, ref := range t.Aux {
		c := j.AdditionalAux[ref.ChainID]
		if ref.ChainID == 98 {
			c = j.DOGE
		}
		if c == nil {
			return nil, errors.New("missing signed auxiliary candidate")
		}
		a := MultiAuxProof{ChainID: ref.ChainID, Header: c.Header, Coinbase: c.Coinbase}
		for _, h := range c.MerkleBranch {
			a.MerkleBranch = append(a.MerkleBranch, common.BytesToHash(h[:]))
		}
		p.Aux = append(p.Aux, a)
	}
	return p, nil
}
func EncodeMultiProof(p *MultiProof) ([]byte, error) {
	if p == nil || len(p.MinerExtra) > 32 {
		return nil, errors.New("invalid multi proof")
	}
	payload, e := rlp.EncodeToBytes(p)
	if e != nil {
		return nil, e
	}
	version := p.Template.Version
	if version != 3 && version != 4 {
		return nil, errors.New("unsupported multi proof version")
	}
	b := append([]byte{'O', 'G', 'G', 'F', version}, payload...)
	if len(b) > MaxOGGProofSize {
		return nil, errors.New("multi proof too large")
	}
	return b, nil
}
func MultiTemplatesEqual(a, b MultiTemplate) bool {
	aa, _ := rlp.EncodeToBytes(a)
	bb, _ := rlp.EncodeToBytes(b)
	return bytes.Equal(aa, bb)
}
func (d *DogeCandidate) AuxValidationBlock() ([]byte, error) {
	if d == nil || len(d.Header) != 80 || len(d.Template.Transactions) > 20000 || len(d.Template.MWEB) != 0 {
		return nil, errors.New("invalid auxiliary proposal")
	}
	placeholder := &DogeAuxPow{ParentCoinbase: d.Coinbase, ParentHeader: make([]byte, 80)}
	wire, e := placeholder.Serialize()
	if e != nil {
		return nil, e
	}
	var out bytes.Buffer
	out.Write(d.Header)
	out.Write(wire)
	writeVarInt(&out, uint64(len(d.Template.Transactions)+1))
	out.Write(d.FullCoinbase)
	for _, tx := range d.Template.Transactions {
		if len(tx.Data) > 4000000 || out.Len()+len(tx.Data) > 4000000 {
			return nil, errors.New("auxiliary proposal too large")
		}
		out.Write(tx.Data)
	}
	if out.Len() > 4000000 {
		return nil, errors.New("auxiliary proposal too large")
	}
	return out.Bytes(), nil
}
func DecodeMultiHex(s string) (*MultiProof, error) {
	b, e := hex.DecodeString(s)
	if e != nil {
		return nil, e
	}
	var p MultiProof
	if len(b) < 5 || !bytes.Equal(b[:4], []byte{'O', 'G', 'G', 'F'}) || (b[4] != 3 && b[4] != 4) || len(b) > MaxOGGProofSize {
		return nil, errors.New("not multi proof")
	}
	e = rlp.DecodeBytes(b[5:], &p)
	if e == nil && p.Template.Version != b[4] {
		return nil, errors.New("multi proof envelope version mismatch")
	}
	return &p, e
}

// RevenueAnchorPresent requires supported DGB or DOGE participation. Bonus
// auxiliary chains cannot satisfy OGG issuance by themselves.
func RevenueAnchorPresent(mask uint8) bool {
	return mask & ^MultiExternalMask == 0 && mask&3 != 0
}
