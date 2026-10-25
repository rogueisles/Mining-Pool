package forgepool

import (
	"bytes"
	"encoding/hex"
	"errors"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

type FlexibleProof struct {
	Multi    MultiProof
	Registry RegistryEnvelope
}

// FlexibleSigningWork binds the exact registry state and administrator approval
// bytes to the mining quorum certificate without changing the RGN AuxPoW leaf.
func FlexibleSigningWork(work common.Hash, envelope *RegistryEnvelope) (common.Hash, error) {
	if work == (common.Hash{}) {
		return common.Hash{}, errors.New("zero flexible work")
	}
	hash, err := RegistryEnvelopeHash(envelope)
	if err != nil {
		return common.Hash{}, err
	}
	blob, err := rlp.EncodeToBytes(struct {
		Domain   []byte
		Work     common.Hash
		Registry common.Hash
	}{[]byte("RGN-FORGE-REGISTRY-WORK-V5"), work, hash})
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(blob), nil
}
func EncodeFlexibleProof(p *FlexibleProof) ([]byte, error) {
	if p == nil || p.Multi.Template.Version != 5 || len(p.Multi.MinerExtra) > MaxOGGMinerExtra {
		return nil, errors.New("invalid flexible proof")
	}
	if err := MultiTemplateValid(p.Multi.Template); err != nil {
		return nil, err
	}
	if _, err := RegistryEnvelopeHash(&p.Registry); err != nil {
		return nil, err
	}
	payload, err := rlp.EncodeToBytes(p)
	if err != nil {
		return nil, err
	}
	out := append([]byte{'O', 'G', 'G', 'F', 5}, payload...)
	if len(out) > MaxOGGProofSize {
		return nil, errors.New("flexible proof too large")
	}
	return out, nil
}
func DecodeFlexibleProof(blob []byte) (*FlexibleProof, error) {
	if len(blob) < 5 || len(blob) > MaxOGGProofSize || !bytes.Equal(blob[:5], []byte{'O', 'G', 'G', 'F', 5}) {
		return nil, errors.New("not a bounded V5 proof")
	}
	var p FlexibleProof
	if err := rlp.DecodeBytes(blob[5:], &p); err != nil {
		return nil, err
	}
	if p.Multi.Template.Version != 5 || len(p.Multi.MinerExtra) > MaxOGGMinerExtra || len(p.Multi.Aux) > MaxFlexibleAuxChains || len(p.Multi.Signatures) > MaxTemplateSigners {
		return nil, errors.New("invalid flexible proof dimensions")
	}
	if err := MultiTemplateValid(p.Multi.Template); err != nil {
		return nil, err
	}
	if _, err := RegistryEnvelopeHash(&p.Registry); err != nil {
		return nil, err
	}
	return &p, nil
}
func DecodeFlexibleHex(s string) (*FlexibleProof, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return DecodeFlexibleProof(b)
}
func (j *DigibyteJob) BindRegistry(envelope *RegistryEnvelope) error {
	if j == nil || j.ProtocolVersion != 5 {
		return errors.New("registry binding requires V5 job")
	}
	work, err := FlexibleSigningWork(common.BytesToHash(j.OGGLeaf[:]), envelope)
	if err != nil {
		return err
	}
	j.CertificateWork = work
	return nil
}
func ValidateFlexibleWorkHeader(header []byte, work, parent common.Hash, stamp, height, activation uint64) error {
	if activation == 0 || height < activation || len(header) == 0 || len(header) > 4096 || crypto.Keccak256Hash(header) != work {
		return errors.New("flexible work header is missing or inactive")
	}
	var fields []rlp.RawValue
	if rlp.DecodeBytes(header, &fields) != nil || len(fields) != 15 {
		return errors.New("invalid flexible work header")
	}
	var number, timestamp uint64
	var previous common.Hash
	var extra []byte
	if rlp.DecodeBytes(fields[0], &previous) != nil || rlp.DecodeBytes(fields[8], &number) != nil || rlp.DecodeBytes(fields[11], &timestamp) != nil || rlp.DecodeBytes(fields[12], &extra) != nil || number != height || previous != parent || timestamp != stamp || len(extra) > 32 {
		return errors.New("flexible work ancestry, height or time mismatch")
	}
	return nil
}

func MultiRequestWork(req MultiSignRequest) (common.Hash, error) {
	if req.Template.Version == 5 {
		return FlexibleSigningWork(req.WorkHash, req.Registry)
	}
	if req.Registry != nil {
		return common.Hash{}, errors.New("legacy work cannot carry registry state")
	}
	return req.WorkHash, nil
}
