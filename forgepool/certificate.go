package forgepool

import (
	"bytes"
	"errors"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

const certificateTag = "RGNCERT4"

func MultiCertificateDigest(t MultiTemplate, work common.Hash, signatures [][]byte) (common.Hash, error) {
	if t.Version != 4 || work == (common.Hash{}) {
		return common.Hash{}, errors.New("invalid certificate domain")
	}
	if err := MultiTemplateValid(t); err != nil {
		return common.Hash{}, err
	}
	payload := struct {
		Domain     []byte
		Template   MultiTemplate
		WorkHash   common.Hash
		Signatures [][]byte
	}{[]byte("RGN-FORGE-CERTIFICATE-V4"), t, work, signatures}
	blob, err := rlp.EncodeToBytes(payload)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(blob), nil
}

func certificateScript(digest common.Hash) []byte {
	script := append([]byte{0x6a, 40}, []byte(certificateTag)...)
	return append(script, digest[:]...)
}

// certificatePlan changes only the uniquely tagged certificate output and
// rebuilds both Stratum fragments from the same transaction representation.
func certificatePlan(source *CoinbasePlan, digest common.Hash) (*CoinbasePlan, error) {
	if source == nil {
		return nil, errors.New("missing certificate coinbase plan")
	}
	plan := *source
	plan.Outputs = append([]TxOutput(nil), source.Outputs...)
	index := -1
	for i, output := range plan.Outputs {
		script := output.Script
		if len(script) < 10 || !bytes.Equal(script[2:10], []byte(certificateTag)) {
			continue
		}
		if index >= 0 || output.Value != 0 || len(script) != 42 || script[0] != 0x6a || script[1] != 40 {
			return nil, errors.New("ambiguous or malformed mined certificate commitment")
		}
		index = i
	}
	if index < 0 {
		return nil, errors.New("missing mined certificate commitment")
	}
	plan.Outputs[index].Script = certificateScript(digest)
	length := len(plan.ScriptPrefix) + plan.ExtraNonce1Size + plan.ExtraNonce2Size + len(plan.ScriptSuffix)
	plan.Coinb1, plan.Coinb2 = plan.baseSplit(length)
	return &plan, nil
}

func BuildMultiParentV4(tpl ParentTemplate, work string, aux map[uint32]*DogeCandidate, script []byte, nonce uint32, extra1, extra2 int, dgb bool) (*DigibyteJob, error) {
	job, err := BuildMultiParent(tpl, work, aux, script, nonce, extra1, extra2, dgb)
	if err != nil {
		return nil, err
	}
	job.Coinbase, err = buildV4CoinbasePlan(tpl, job.Commitment, script, extra1, extra2)
	if err != nil {
		return nil, err
	}
	job.ProtocolVersion = 4
	job.Coinbase.Outputs = append(job.Coinbase.Outputs, TxOutput{Value: 0, Script: certificateScript(common.Hash{})})
	job.Coinbase, err = certificatePlan(job.Coinbase, common.Hash{})
	return job, err
}

// buildV4CoinbasePlan confines variable mining bytes to a single data push.
// Fixed push boundaries keep native signature-operation counts independent
// of the extranonce selected after the template has been approved.
func buildV4CoinbasePlan(tpl ParentTemplate, commitment, payout []byte, extra1, extra2 int) (*CoinbasePlan, error) {
	if extra1 != 4 || extra2 != 4 || len(commitment) != 44 {
		return nil, errors.New("V4 requires an eight-byte extranonce and a complete auxiliary commitment")
	}
	height, err := BIP34Height(tpl.Height)
	if err != nil {
		return nil, err
	}
	prefix := append([]byte(nil), height...)
	aux := SortedCoinbaseAux(tpl.CoinbaseAux)
	if len(aux) != 0 {
		pushed, err := PushData(aux)
		if err != nil {
			return nil, err
		}
		prefix = append(prefix, pushed...)
	}
	pushed, err := PushData(commitment)
	if err != nil {
		return nil, err
	}
	prefix = append(prefix, pushed...)
	prefix = append(prefix, byte(extra1+extra2))
	length := len(prefix) + extra1 + extra2
	if length < 2 || length > 100 || bytes.Count(prefix, MergedMiningMagic) != 1 {
		return nil, errors.New("V4 coinbase script exceeds its bounds or has an ambiguous auxiliary commitment")
	}
	plan, err := BuildCoinbasePlan(tpl.Height, nil, nil, payout, tpl.CoinbaseValue, tpl.WitnessCommitment, extra1, extra2)
	if err != nil {
		return nil, err
	}
	plan.ScriptPrefix = prefix
	plan.Coinb1, plan.Coinb2 = plan.baseSplit(length)
	return plan, nil
}

func (j *DigibyteJob) BindCertificate(t MultiTemplate, signatures [][]byte) error {
	if j.ProtocolVersion != 4 || t.Version != 4 {
		return errors.New("certificate requires a V4 parent job")
	}
	rebuilt, err := j.MultiTemplate(t.ValidFrom, t.Expires)
	if err != nil || !MultiTemplatesEqual(rebuilt, t) {
		return errors.New("certificate does not match parent template")
	}
	digest, err := MultiCertificateDigest(t, common.BytesToHash(j.OGGLeaf[:]), signatures)
	if err != nil {
		return err
	}
	j.Coinbase, err = certificatePlan(j.Coinbase, digest)
	return err
}

func (j *DigibyteJob) CheckCertificate(t MultiTemplate, signatures [][]byte) error {
	if j.ProtocolVersion != 4 || t.Version != 4 {
		return errors.New("certificate requires a V4 parent job")
	}
	digest, err := MultiCertificateDigest(t, common.BytesToHash(j.OGGLeaf[:]), signatures)
	if err != nil {
		return err
	}
	want, err := certificatePlan(j.Coinbase, digest)
	if err != nil || !bytes.Equal(want.Coinb1, j.Coinbase.Coinb1) || !bytes.Equal(want.Coinb2, j.Coinbase.Coinb2) {
		return errors.New("parent is not bound to the supplied certificate")
	}
	return nil
}

// CanonicalMultiSignatures orders distinct signers before their certificate is
// committed to mining work. Signature authorization is checked separately.
func CanonicalMultiSignatures(t MultiTemplate, work common.Hash, signatures [][]byte) ([][]byte, error) {
	digest, err := MultiDigest(t, work)
	if err != nil || t.Version != 4 || len(signatures) == 0 || len(signatures) > MaxTemplateSigners {
		return nil, errors.New("invalid certificate signature set")
	}
	type signed struct {
		address   common.Address
		signature []byte
	}
	items := make([]signed, 0, len(signatures))
	seen := map[common.Address]bool{}
	for _, signature := range signatures {
		if len(signature) != 65 {
			return nil, errors.New("invalid certificate signature length")
		}
		pub, err := crypto.SigToPub(digest.Bytes(), signature)
		if err != nil {
			return nil, err
		}
		address := crypto.PubkeyToAddress(*pub)
		if seen[address] {
			return nil, errors.New("duplicate certificate signer")
		}
		seen[address] = true
		items = append(items, signed{address, append([]byte(nil), signature...)})
	}
	sort.Slice(items, func(i, j int) bool { return bytes.Compare(items[i].address[:], items[j].address[:]) < 0 })
	out := make([][]byte, len(items))
	for i := range items {
		out[i] = items[i].signature
	}
	return out, nil
}
