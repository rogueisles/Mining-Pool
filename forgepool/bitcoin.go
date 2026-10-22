package forgepool

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

// TemplateTx is a Bitcoin-family template transaction. TxID is kept in the
// raw/internal byte order used by Merkle hashing, while Data is the serialized
// transaction returned by getblocktemplate.
type TemplateTx struct {
	Data []byte
	TxID Hash
}

// ParentTemplate is the chain-neutral subset of a Bitcoin-family
// getblocktemplate needed by FORGE job construction.
type ParentTemplate struct {
	Version           int32
	Previous          Hash
	Transactions      []TemplateTx
	CoinbaseAux       map[string][]byte
	CoinbaseValue     int64
	CurTime           uint32
	Bits              uint32
	Height            int64
	WitnessCommitment []byte
	MWEB              []byte
}

// TxOutput is a serialized Bitcoin-family transaction output.
type TxOutput struct {
	Value  int64
	Script []byte
}

// CoinbasePlan describes a deterministic coinbase whose extranonce fields are
// the only bytes changed by Stratum miners. Coinb1/Coinb2 are the base
// (non-witness) serialization used to derive the coinbase txid and Merkle root.
type CoinbasePlan struct {
	Version         int32
	ScriptPrefix    []byte
	ScriptSuffix    []byte
	ExtraNonce1Size int
	ExtraNonce2Size int
	Outputs         []TxOutput
	Witness         bool
	Coinb1          []byte
	Coinb2          []byte
}

func writeVarInt(buf *bytes.Buffer, n uint64) {
	switch {
	case n < 0xfd:
		buf.WriteByte(byte(n))
	case n <= 0xffff:
		buf.WriteByte(0xfd)
		_ = binary.Write(buf, binary.LittleEndian, uint16(n))
	case n <= 0xffffffff:
		buf.WriteByte(0xfe)
		_ = binary.Write(buf, binary.LittleEndian, uint32(n))
	default:
		buf.WriteByte(0xff)
		_ = binary.Write(buf, binary.LittleEndian, n)
	}
}

func encodeVarInt(n uint64) []byte {
	var b bytes.Buffer
	writeVarInt(&b, n)
	return b.Bytes()
}

// ScriptNum encodes an integer using Bitcoin's minimally encoded CScriptNum
// representation (without the push opcode).
func ScriptNum(v int64) []byte {
	if v == 0 {
		return nil
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var out []byte
	for v > 0 {
		out = append(out, byte(v&0xff))
		v >>= 8
	}
	if out[len(out)-1]&0x80 != 0 {
		if neg {
			out = append(out, 0x80)
		} else {
			out = append(out, 0)
		}
	} else if neg {
		out[len(out)-1] |= 0x80
	}
	return out
}

// PushData returns a minimally encoded direct push for short coinbase payloads.
func PushData(data []byte) ([]byte, error) {
	if len(data) > 75 {
		return nil, errors.New("FORGE coinbase push exceeds direct-push range")
	}
	out := make([]byte, 1+len(data))
	out[0] = byte(len(data))
	copy(out[1:], data)
	return out, nil
}

// BIP34Height returns the minimally pushed block-height prefix required in a
// version-2+ coinbase scriptSig.
func BIP34Height(height int64) ([]byte, error) {
	if height < 0 {
		return nil, errors.New("negative BIP34 height")
	}
	return PushData(ScriptNum(height))
}

// SortedCoinbaseAux concatenates getblocktemplate coinbaseaux values in stable
// key order. Values are already decoded script bytes.
func SortedCoinbaseAux(aux map[string][]byte) []byte {
	if len(aux) == 0 {
		return nil
	}
	keys := make([]string, 0, len(aux))
	for k := range aux {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []byte
	for _, k := range keys {
		out = append(out, aux[k]...)
	}
	return out
}

func serializeOutputs(outputs []TxOutput) []byte {
	var b bytes.Buffer
	writeVarInt(&b, uint64(len(outputs)))
	for _, out := range outputs {
		_ = binary.Write(&b, binary.LittleEndian, uint64(out.Value))
		writeVarInt(&b, uint64(len(out.Script)))
		b.Write(out.Script)
	}
	return b.Bytes()
}

// BuildCoinbasePlan constructs a coinbase with a fixed-length extranonce hole.
// payload is appended verbatim after BIP34+coinbaseaux; this is where the
// standard merged-mining commitment is placed for the Digibyte parent.
func BuildCoinbasePlan(height int64, aux map[string][]byte, payload, payoutScript []byte, value int64, witnessCommitment []byte, extraNonce1Size, extraNonce2Size int) (*CoinbasePlan, error) {
	if value < 0 {
		return nil, errors.New("negative coinbase value")
	}
	if len(payoutScript) == 0 {
		return nil, errors.New("empty FORGE payout script")
	}
	if extraNonce1Size < 0 || extraNonce2Size < 0 {
		return nil, errors.New("negative extranonce size")
	}
	heightPush, err := BIP34Height(height)
	if err != nil {
		return nil, err
	}
	prefix := append([]byte{}, heightPush...)
	prefix = append(prefix, SortedCoinbaseAux(aux)...)
	prefix = append(prefix, payload...)
	scriptLen := len(prefix) + extraNonce1Size + extraNonce2Size
	if scriptLen < 2 || scriptLen > 100 {
		return nil, fmt.Errorf("coinbase scriptSig length %d outside consensus range", scriptLen)
	}

	outputs := []TxOutput{{Value: value, Script: append([]byte{}, payoutScript...)}}
	witness := len(witnessCommitment) != 0
	if witness {
		outputs = append(outputs, TxOutput{Value: 0, Script: append([]byte{}, witnessCommitment...)})
	}

	plan := &CoinbasePlan{
		Version:         2,
		ScriptPrefix:    prefix,
		ExtraNonce1Size: extraNonce1Size,
		ExtraNonce2Size: extraNonce2Size,
		Outputs:         outputs,
		Witness:         witness,
	}
	plan.Coinb1, plan.Coinb2 = plan.baseSplit(scriptLen)
	return plan, nil
}

func (p *CoinbasePlan) baseSplit(scriptLen int) ([]byte, []byte) {
	var one bytes.Buffer
	_ = binary.Write(&one, binary.LittleEndian, uint32(p.Version))
	writeVarInt(&one, 1)
	one.Write(make([]byte, 32))
	_ = binary.Write(&one, binary.LittleEndian, uint32(0xffffffff))
	writeVarInt(&one, uint64(scriptLen))
	one.Write(p.ScriptPrefix)

	var two bytes.Buffer
	two.Write(p.ScriptSuffix)
	_ = binary.Write(&two, binary.LittleEndian, uint32(0xffffffff))
	two.Write(serializeOutputs(p.Outputs))
	_ = binary.Write(&two, binary.LittleEndian, uint32(0))
	return one.Bytes(), two.Bytes()
}

func (p *CoinbasePlan) validateExtra(extra1, extra2 []byte) error {
	if len(extra1) != p.ExtraNonce1Size {
		return fmt.Errorf("extranonce1 length %d want %d", len(extra1), p.ExtraNonce1Size)
	}
	if len(extra2) != p.ExtraNonce2Size {
		return fmt.Errorf("extranonce2 length %d want %d", len(extra2), p.ExtraNonce2Size)
	}
	return nil
}

// BaseCoinbase returns the non-witness serialization used for the txid.
func (p *CoinbasePlan) BaseCoinbase(extra1, extra2 []byte) ([]byte, error) {
	if err := p.validateExtra(extra1, extra2); err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(p.Coinb1)+len(extra1)+len(extra2)+len(p.Coinb2))
	out = append(out, p.Coinb1...)
	out = append(out, extra1...)
	out = append(out, extra2...)
	out = append(out, p.Coinb2...)
	return out, nil
}

// FullCoinbase returns the block-wire transaction. If a witness commitment is
// present it includes marker/flag and a single 32-byte zero witness-reserved
// value, while preserving the same base txid.
func (p *CoinbasePlan) FullCoinbase(extra1, extra2 []byte) ([]byte, error) {
	if err := p.validateExtra(extra1, extra2); err != nil {
		return nil, err
	}
	script := append([]byte{}, p.ScriptPrefix...)
	script = append(script, extra1...)
	script = append(script, extra2...)
	script = append(script, p.ScriptSuffix...)

	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, uint32(p.Version))
	if p.Witness {
		b.WriteByte(0x00)
		b.WriteByte(0x01)
	}
	writeVarInt(&b, 1)
	b.Write(make([]byte, 32))
	_ = binary.Write(&b, binary.LittleEndian, uint32(0xffffffff))
	writeVarInt(&b, uint64(len(script)))
	b.Write(script)
	_ = binary.Write(&b, binary.LittleEndian, uint32(0xffffffff))
	b.Write(serializeOutputs(p.Outputs))
	if p.Witness {
		writeVarInt(&b, 1)
		writeVarInt(&b, 32)
		b.Write(make([]byte, 32))
	}
	_ = binary.Write(&b, binary.LittleEndian, uint32(0))
	return b.Bytes(), nil
}

func CoinbaseTxID(baseCoinbase []byte) Hash { return doubleSHA256(baseCoinbase) }

// CoinbaseMerkleBranch returns the Merkle siblings for leaf index zero. The
// branch is independent of the unknown coinbase txid and can therefore be sent
// directly in a Stratum job.
func CoinbaseMerkleBranch(txs []TemplateTx) []Hash {
	if len(txs) == 0 {
		return nil
	}
	nodes := make([]Hash, 1, len(txs)+1)
	nodes[0] = Hash{} // unknown coinbase placeholder
	for _, tx := range txs {
		nodes = append(nodes, tx.TxID)
	}
	idx := 0
	var branch []Hash
	for len(nodes) > 1 {
		if len(nodes)%2 == 1 {
			nodes = append(nodes, nodes[len(nodes)-1])
		}
		branch = append(branch, nodes[idx^1])
		next := make([]Hash, len(nodes)/2)
		for i := 0; i < len(nodes); i += 2 {
			pair := make([]byte, 0, 64)
			pair = append(pair, nodes[i][:]...)
			pair = append(pair, nodes[i+1][:]...)
			next[i/2] = doubleSHA256(pair)
		}
		nodes = next
		idx /= 2
	}
	return branch
}

// MerkleRootFromBranch folds a raw/internal hash through a Merkle branch.
func MerkleRootFromBranch(leaf Hash, branch []Hash, index uint32) Hash {
	root := leaf
	for _, sibling := range branch {
		pair := make([]byte, 0, 64)
		if index&1 == 0 {
			pair = append(pair, root[:]...)
			pair = append(pair, sibling[:]...)
		} else {
			pair = append(pair, sibling[:]...)
			pair = append(pair, root[:]...)
		}
		root = doubleSHA256(pair)
		index >>= 1
	}
	return root
}

// MerkleRoot computes a complete Bitcoin-family Merkle tree. It is useful for
// independent cross-checks of the optimized coinbase-branch path.
func MerkleRoot(leaves []Hash) (Hash, error) {
	if len(leaves) == 0 {
		return Hash{}, errors.New("empty Merkle tree")
	}
	nodes := append([]Hash{}, leaves...)
	for len(nodes) > 1 {
		if len(nodes)%2 == 1 {
			nodes = append(nodes, nodes[len(nodes)-1])
		}
		next := make([]Hash, len(nodes)/2)
		for i := 0; i < len(nodes); i += 2 {
			pair := append(append(make([]byte, 0, 64), nodes[i][:]...), nodes[i+1][:]...)
			next[i/2] = doubleSHA256(pair)
		}
		nodes = next
	}
	return nodes[0], nil
}

// SerializeHeader returns the exact 80-byte Bitcoin-family parent header.
func SerializeHeader(version int32, previous, merkleRoot Hash, ntime, bits, nonce uint32) []byte {
	out := make([]byte, 80)
	binary.LittleEndian.PutUint32(out[0:4], uint32(version))
	copy(out[4:36], previous[:])
	copy(out[36:68], merkleRoot[:])
	binary.LittleEndian.PutUint32(out[68:72], ntime)
	binary.LittleEndian.PutUint32(out[72:76], bits)
	binary.LittleEndian.PutUint32(out[76:80], nonce)
	return out
}

// SerializeBlock assembles a Bitcoin/Digibyte family block. Current Digibyte
// MWEB templates serialize an optional MWEB block after the transaction vector;
// getblocktemplate supplies the inner serialized MWEB bytes, so a present-byte
// (0x01) is prefixed here before those raw bytes.
func SerializeBlock(header, fullCoinbase []byte, txs []TemplateTx, mweb []byte, hasWitnessCommitment bool) ([]byte, error) {
	if len(header) != 80 {
		return nil, fmt.Errorf("invalid parent header length %d", len(header))
	}
	if len(mweb) != 0 && !hasWitnessCommitment {
		return nil, errors.New("MWEB template missing witness commitment")
	}
	var b bytes.Buffer
	b.Write(header)
	writeVarInt(&b, uint64(len(txs)+1))
	b.Write(fullCoinbase)
	for _, tx := range txs {
		if len(tx.Data) == 0 {
			return nil, errors.New("template transaction missing data")
		}
		b.Write(tx.Data)
	}
	if len(mweb) != 0 {
		b.WriteByte(0x01)
		b.Write(mweb)
	}
	return b.Bytes(), nil
}
