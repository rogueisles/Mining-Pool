package forgepool

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const dogeAuxPowFlag uint32 = 1 << 8

// ParseRawHashHex decodes an OGG common.Hash-style hexadecimal value without
// Bitcoin display-byte reversal. This is intentionally different from
// ParseDisplayHash: OGG WorkHash bytes are inserted directly as an Aux leaf.
func ParseRawHashHex(s string) (Hash, error) {
	var out Hash
	s = strings.TrimPrefix(s, "0x")
	b, err := hex.DecodeString(s)
	if err != nil {
		return out, err
	}
	if len(b) != len(out) {
		return out, fmt.Errorf("invalid raw hash length %d", len(b))
	}
	copy(out[:], b)
	return out, nil
}

func ParseBitsHex(s string) (uint32, error) {
	s = strings.TrimPrefix(s, "0x")
	v, err := strconv.ParseUint(s, 16, 32)
	return uint32(v), err
}

// DogeCandidate is the deterministic DOGE AuxPoW child candidate committed by
// the Digibyte parent job and later carried in the OGG proof envelope.
type DogeCandidate struct {
	Template     ParentTemplate
	Version      int32
	Coinbase     []byte
	FullCoinbase []byte
	CoinbaseTxID Hash
	MerkleBranch []Hash
	MerkleRoot   Hash
	Header       []byte
	Hash         Hash
	DisplayHash  string
	Bits         uint32
	JobNonce     uint32
}

// BuildDogeCandidate constructs a normal DOGE block candidate whose version
// carries the AuxPoW flag and DOGE chain ID. Its coinbase pays only the
// configured FORGE protocol payout script.
func BuildDogeCandidate(tpl ParentTemplate, payoutScript []byte, jobNonce uint32) (*DogeCandidate, error) {
	var nonceBytes [4]byte
	binary.LittleEndian.PutUint32(nonceBytes[:], jobNonce)
	noncePush, err := PushData(nonceBytes[:])
	if err != nil {
		return nil, err
	}
	plan, err := BuildCoinbasePlan(tpl.Height, tpl.CoinbaseAux, noncePush, payoutScript, tpl.CoinbaseValue, tpl.WitnessCommitment, 0, 0)
	if err != nil {
		return nil, err
	}
	coinbase, err := plan.BaseCoinbase(nil, nil)
	if err != nil {
		return nil, err
	}
	fullCoinbase, err := plan.FullCoinbase(nil, nil)
	if err != nil {
		return nil, err
	}
	coinbaseID := CoinbaseTxID(coinbase)
	branch := CoinbaseMerkleBranch(tpl.Transactions)
	root := MerkleRootFromBranch(coinbaseID, branch, 0)
	version := int32((uint32(tpl.Version) & 0x0000ffff) | dogeAuxPowFlag | (DOGEAuxChainID << 16))
	header := SerializeHeader(version, tpl.Previous, root, tpl.CurTime, tpl.Bits, 0)
	hash := doubleSHA256(header)
	return &DogeCandidate{
		Template: tpl, Version: version, Coinbase: coinbase, FullCoinbase: fullCoinbase, CoinbaseTxID: coinbaseID,
		MerkleBranch: branch, MerkleRoot: root, Header: header, Hash: hash,
		DisplayHash: DisplayHash(hash), Bits: tpl.Bits, JobNonce: jobNonce,
	}, nil
}

// DigibyteJob is the merged parent work distributed over Scrypt Stratum.
type DigibyteJob struct {
	ProtocolVersion uint8
	Template        ParentTemplate
	OGGLeaf         Hash
	DOGE            *DogeCandidate
	AuxNonce        uint32
	AuxTree         *AuxTree
	AdditionalAux   map[uint32]*DogeCandidate
	OGGIndex        uint32
	DOGEIndex       uint32
	Commitment      []byte
	Coinbase        *CoinbasePlan
	MerkleBranch    []Hash
	PayoutScript    []byte
	ExtraNonce1Size int
	ExtraNonce2Size int

	// DigibyteActive distinguishes a real Digibyte getblocktemplate parent from
	// the FORGE-native synthetic Scrypt parent used when Digibyte is unavailable.
	DigibyteActive bool
}

// BuildDigibyteJob preserves the exact V1 behavior: both Digibyte and DOGE
// participate and the real Digibyte template is the Scrypt parent.
func BuildDigibyteJob(tpl ParentTemplate, oggWorkHash string, doge *DogeCandidate, payoutScript []byte, auxNonceSeed uint32, extraNonce1Size, extraNonce2Size int) (*DigibyteJob, error) {
	if doge == nil {
		return nil, errors.New("missing DOGE candidate")
	}
	return BuildParentJobV2(tpl, oggWorkHash, doge, payoutScript, auxNonceSeed, extraNonce1Size, extraNonce2Size, true)
}

// BuildParentJobV2 constructs the Scrypt parent used by FORGE V2.
//
// digibyteActive=true means tpl came from Digibyte and a winning Digibyte hit
// may be submitted there. digibyteActive=false means tpl is a synthetic FORGE
// parent which MUST NEVER be submitted as a Digibyte block.
//
// DOGE is independently optional in either mode.
func BuildParentJobV2(tpl ParentTemplate, oggWorkHash string, doge *DogeCandidate, payoutScript []byte, auxNonceSeed uint32, extraNonce1Size, extraNonce2Size int, digibyteActive bool) (*DigibyteJob, error) {
	oggLeaf, err := ParseRawHashHex(oggWorkHash)
	if err != nil {
		return nil, fmt.Errorf("invalid OGG WorkHash: %v", err)
	}

	auxNonce := auxNonceSeed
	if doge != nil {
		auxNonce = DeterministicAuxNonce(auxNonceSeed)
	}

	var dogeLeaf *Hash
	if doge != nil {
		dogeLeaf = &doge.Hash
	}

	commitment, oi, di, err := AuxCommitmentV2(oggLeaf, dogeLeaf, auxNonce)
	if err != nil {
		return nil, err
	}

	if len(tpl.MWEB) != 0 && len(tpl.WitnessCommitment) == 0 {
		return nil, errors.New("MWEB template missing witness commitment")
	}

	plan, err := BuildCoinbasePlan(
		tpl.Height,
		tpl.CoinbaseAux,
		commitment,
		payoutScript,
		tpl.CoinbaseValue,
		tpl.WitnessCommitment,
		extraNonce1Size,
		extraNonce2Size,
	)
	if err != nil {
		return nil, err
	}

	return &DigibyteJob{
		Template:        tpl,
		OGGLeaf:         oggLeaf,
		DOGE:            doge,
		AuxNonce:        auxNonce,
		OGGIndex:        oi,
		DOGEIndex:       di,
		Commitment:      commitment,
		Coinbase:        plan,
		MerkleBranch:    CoinbaseMerkleBranch(tpl.Transactions),
		PayoutScript:    append([]byte{}, payoutScript...),
		ExtraNonce1Size: extraNonce1Size,
		ExtraNonce2Size: extraNonce2Size,
		DigibyteActive:  digibyteActive,
	}, nil
}

// SyntheticParentTemplate returns the deterministic Bitcoin-family template
// used purely as the FORGE Scrypt PoW carrier while Digibyte is unavailable.
//
// It is deliberately impossible to confuse with current Digibyte work:
// previous hash is zero, it carries no external transactions/reward, and
// DigibyteActive is false on the resulting job.
func SyntheticParentTemplate(height int64, ntime uint32) (ParentTemplate, error) {
	if height <= 0 {
		return ParentTemplate{}, errors.New("invalid synthetic parent height")
	}
	if ntime == 0 {
		return ParentTemplate{}, errors.New("invalid synthetic parent time")
	}

	return ParentTemplate{
		Version:       1,
		Previous:      Hash{},
		Transactions:  nil,
		CoinbaseAux:   nil,
		CoinbaseValue: 0,
		CurTime:       ntime,

		// Bitcoin diff-1 compact bits keep the Stratum field conventional.
		// OGG consensus uses its own signed/header difficulty target and does
		// not treat this field as Digibyte difficulty when DGB is inactive.
		Bits:   0x1d00ffff,
		Height: height,
	}, nil
}

// Header reconstructs the exact 80-byte Digibyte Scrypt header for a Stratum
// submission.
func (j *DigibyteJob) Header(extra1, extra2 []byte, ntime, nonce uint32) ([]byte, error) {
	coinbase, err := j.Coinbase.BaseCoinbase(extra1, extra2)
	if err != nil {
		return nil, err
	}
	root := MerkleRootFromBranch(CoinbaseTxID(coinbase), j.MerkleBranch, 0)
	return SerializeHeader(j.Template.Version, j.Template.Previous, root, ntime, j.Template.Bits, nonce), nil
}

// FullBlock reconstructs a winning Digibyte block including the witness
// coinbase and the current Digibyte MWEB extension when present.
func (j *DigibyteJob) FullBlock(extra1, extra2 []byte, ntime, nonce uint32) ([]byte, error) {
	if !j.DigibyteActive {
		return nil, errors.New("synthetic FORGE parent cannot be submitted to Digibyte")
	}
	header, err := j.Header(extra1, extra2, ntime, nonce)
	if err != nil {
		return nil, err
	}
	coinbase, err := j.Coinbase.FullCoinbase(extra1, extra2)
	if err != nil {
		return nil, err
	}
	return SerializeBlock(header, coinbase, j.Template.Transactions, j.Template.MWEB, len(j.Template.WitnessCommitment) != 0)
}
