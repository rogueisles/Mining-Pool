// forge-registry-admin prepares and signs public registry requests offline.
// It never connects to RPC, generates production keys, submits transactions,
// or writes a private key into an approval or mining configuration.
package main

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"math/big"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

const maxPublicFile = 131072

type approvalRequest struct {
	Version          uint8                    `json:"version"`
	State            rpc.ForgeRegistryReply   `json:"state"`
	Change           forgepool.RegistryChange `json:"change"`
	PreviewHeight    uint64                   `json:"previewHeight"`
	Digest           common.Hash              `json:"digest"`
	AcceptanceDigest common.Hash              `json:"acceptanceDigest"`
}

type approvalSignature struct {
	Version    uint8          `json:"version"`
	ChainID    string         `json:"chainId"`
	Genesis    common.Hash    `json:"genesis"`
	ParentRoot common.Hash    `json:"parentRoot"`
	Digest     common.Hash    `json:"digest"`
	Signer     common.Address `json:"signer"`
	Acceptance bool           `json:"acceptance"`
	Signature  hexutil.Bytes  `json:"signature"`
}

type requestView struct {
	NetworkChainID                   string           `json:"networkChainId"`
	NetworkGenesis                   common.Hash      `json:"networkGenesis"`
	ParentHash                       common.Hash      `json:"parentHash"`
	ParentRegistryRoot               common.Hash      `json:"parentRegistryRoot"`
	ParentCheckpoint                 uint64           `json:"parentCheckpoint"`
	PreviewInclusionHeight           uint64           `json:"previewInclusionHeight"`
	LatestInclusionHeight            uint64           `json:"latestInclusionHeight"`
	ActivationHeight                 uint64           `json:"activationHeight"`
	Kind                             string           `json:"kind"`
	Slot                             uint8            `json:"slot,omitempty"`
	NativeChainID                    uint32           `json:"nativeChainId"`
	NativeGenesis                    common.Hash      `json:"nativeGenesis"`
	TreasuryScriptHash               common.Hash      `json:"treasuryScriptHash"`
	CurrentAdministrators            []common.Address `json:"currentAdministrators"`
	CurrentQuorum                    uint64           `json:"currentQuorum"`
	NewAdministrators                []common.Address `json:"newAdministrators,omitempty"`
	NewQuorum                        uint64           `json:"newQuorum,omitempty"`
	ApprovalDigest                   common.Hash      `json:"approvalDigest"`
	NewAdministratorAcceptanceDigest common.Hash      `json:"newAdministratorAcceptanceDigest"`
	Notice                           string           `json:"notice"`
}

type stringFlags []string

func (s *stringFlags) String() string         { return strings.Join(*s, ",") }
func (s *stringFlags) Set(value string) error { *s = append(*s, value); return nil }

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Registry administration:", err)
		os.Exit(1)
	}
}

func readPublic(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("unable to open public input file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxPublicFile {
		return nil, errors.New("public input must be a bounded regular file")
	}
	b, err := ioutil.ReadAll(io.LimitReader(f, maxPublicFile+1))
	if err != nil || len(b) > maxPublicFile {
		return nil, errors.New("public input exceeds the file limit")
	}
	return b, nil
}

func decodeJSON(data []byte, target interface{}) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return errors.New("invalid or unsupported public JSON fields")
	}
	var extra interface{}
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("public JSON contains trailing data")
	}
	return nil
}

func readJSON(path string, target interface{}) error {
	b, err := readPublic(path)
	if err != nil {
		return err
	}
	return decodeJSON(b, target)
}

func writeJSON(path string, value interface{}) error {
	if path == "" {
		return errors.New("an output path is required")
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return errors.New("unable to encode public output")
	}
	b = append(b, '\n')
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("output must be a new writable file; existing files are never overwritten")
	}
	_, err = f.Write(b)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.New("unable to save public output")
	}
	return nil
}

func parseHash(text string) (common.Hash, error) {
	b, err := hexutil.Decode(text)
	if err != nil || len(b) != 32 {
		return common.Hash{}, errors.New("expected an exact 0x-prefixed 32-byte hash")
	}
	return common.BytesToHash(b), nil
}

func loadState(path string) (rpc.ForgeRegistryReply, error) {
	var state rpc.ForgeRegistryReply
	b, err := readPublic(path)
	if err != nil {
		return state, err
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal(b, &probe) != nil {
		return state, errors.New("invalid saved registry state")
	}
	if raw, ok := probe["result"]; ok {
		var envelope struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  json.RawMessage `json:"result"`
			Error   json.RawMessage `json:"error"`
		}
		if decodeJSON(b, &envelope) != nil || envelope.JSONRPC != "2.0" || (len(envelope.Error) != 0 && string(envelope.Error) != "null") {
			return state, errors.New("saved RPC response is not successful JSON-RPC 2.0")
		}
		b = raw
	}
	if err = decodeJSON(b, &state); err != nil {
		return state, err
	}
	if state.ParentHash == (common.Hash{}) {
		return state, errors.New("saved state must identify an exact parent hash")
	}
	return state, nil
}

func activeAuthority(snapshot forgepool.RegistrySnapshot, height uint64) ([]common.Address, uint64) {
	admin := snapshot.Admin
	if admin.NextHeight != 0 && height >= admin.NextHeight {
		return admin.NextKeys, admin.NextQuorum
	}
	return admin.Keys, admin.Quorum
}

func contains(keys []common.Address, address common.Address) bool {
	for _, key := range keys {
		if key == address {
			return true
		}
	}
	return false
}

func validateRequest(request *approvalRequest) (forgepool.RegistrySnapshot, forgepool.RegistryContext, forgepool.RegistrySnapshot, error) {
	var empty forgepool.RegistrySnapshot
	var emptyContext forgepool.RegistryContext
	if request == nil || request.Version != 1 || request.State.ParentHash == (common.Hash{}) {
		return empty, emptyContext, empty, errors.New("unsupported approval request")
	}
	snapshot, ctx, parentHeight, activation, err := request.State.Decode()
	if err != nil {
		return empty, ctx, empty, err
	}
	if parentHeight == math.MaxUint64 || request.PreviewHeight <= parentHeight || request.PreviewHeight < activation {
		return empty, ctx, empty, errors.New("preview inclusion height is not a future upgraded block")
	}
	after, err := forgepool.RegistryApplyChange(snapshot, request.PreviewHeight, request.Change, ctx)
	if err != nil {
		return empty, ctx, empty, err
	}
	digest, err := forgepool.RegistryOperationDigest(request.State.Root, request.PreviewHeight, request.Change, ctx)
	if err != nil || digest != request.Digest {
		return empty, ctx, empty, errors.New("request approval digest does not match its network, parent state, and requested change")
	}
	if request.Change.Kind == forgepool.RegistryRotateAdmins {
		digest, err = forgepool.RegistryAcceptanceDigest(request.State.Root, request.PreviewHeight, request.Change, ctx)
		if err != nil || digest != request.AcceptanceDigest {
			return empty, ctx, empty, errors.New("request administrator acceptance digest mismatch")
		}
	} else if request.AcceptanceDigest != (common.Hash{}) {
		return empty, ctx, empty, errors.New("non-rotation request carries administrator acceptance digest")
	}
	return snapshot, ctx, after, nil
}

func reviewRequest(request *approvalRequest) (requestView, error) {
	snapshot, ctx, _, err := validateRequest(request)
	if err != nil {
		return requestView{}, err
	}
	keys, quorum := activeAuthority(snapshot, request.PreviewHeight)
	view := requestView{NetworkChainID: ctx.ChainID.String(), NetworkGenesis: ctx.Genesis, ParentHash: request.State.ParentHash, ParentRegistryRoot: request.State.Root, ParentCheckpoint: ctx.ParentCheckpoint, PreviewInclusionHeight: request.PreviewHeight, LatestInclusionHeight: request.Change.ActivationHeight - ctx.MinDelay, ActivationHeight: request.Change.ActivationHeight, CurrentAdministrators: keys, CurrentQuorum: quorum, ApprovalDigest: request.Digest, NewAdministratorAcceptanceDigest: request.AcceptanceDigest, Notice: "Offline review only. Independently verify the network and parent root from a trusted node. Treasury changes affect future mining destinations; existing funds do not move. Pool software recomputes the result root for the actual inclusion height."}
	view.Slot = request.Change.Slot
	switch request.Change.Kind {
	case forgepool.RegistryRegister:
		view.Kind = "register-compatible-coin"
		view.Slot = request.Change.Coin.Slot
		view.NativeChainID = request.Change.Coin.ChainID
		view.NativeGenesis = request.Change.Coin.Genesis
		view.TreasuryScriptHash = request.Change.Coin.Treasury
	case forgepool.RegistryRotateTreasury:
		view.Kind = "rotate-treasury"
		view.TreasuryScriptHash = request.Change.Treasury
	case forgepool.RegistryDisable:
		view.Kind = "disable-optional-coin"
	case forgepool.RegistryRotateAdmins:
		view.Kind = "rotate-administrators"
		view.NewAdministrators = request.Change.Keys
		view.NewQuorum = request.Change.Quorum
	}
	if request.Change.Kind != forgepool.RegistryRegister {
		for _, coin := range snapshot.Coins {
			if coin.Slot == request.Change.Slot {
				view.NativeChainID = coin.ChainID
				view.NativeGenesis = coin.Genesis
			}
		}
	}
	return view, nil
}

func printReview(out io.Writer, request *approvalRequest) error {
	view, err := reviewRequest(request)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(view)
}

func loadPrivateKey(path string) (*ecdsa.PrivateKey, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return nil, errors.New("private key must be a small regular local file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private key file permissions must exclude other users (chmod 600)")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("unable to read private key file")
	}
	defer f.Close()
	b, err := ioutil.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(b) > 4096 {
		return nil, errors.New("invalid private key file")
	}
	defer func() {
		for i := range b {
			b[i] = 0
		}
	}()
	text := strings.TrimSpace(string(b))
	text = strings.TrimPrefix(text, "0x")
	if len(text) != 64 {
		return nil, errors.New("private key file must contain only one 32-byte hexadecimal key")
	}
	key, err := crypto.HexToECDSA(text)
	if err != nil {
		return nil, errors.New("invalid private key file")
	}
	return key, nil
}

func recoverSignature(signature approvalSignature, request *approvalRequest, ctx forgepool.RegistryContext) (common.Address, error) {
	digest := request.Digest
	if signature.Acceptance {
		if request.Change.Kind != forgepool.RegistryRotateAdmins {
			return common.Address{}, errors.New("unexpected administrator acceptance signature")
		}
		digest = request.AcceptanceDigest
	}
	if signature.Version != 1 || signature.ChainID != ctx.ChainID.String() || signature.Genesis != ctx.Genesis || signature.ParentRoot != request.State.Root || signature.Digest != digest || len(signature.Signature) != 65 {
		return common.Address{}, errors.New("signature belongs to another request or network")
	}
	sig := []byte(signature.Signature)
	if !crypto.ValidateSignatureValues(sig[64], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:64]), true) {
		return common.Address{}, errors.New("invalid canonical administrator signature")
	}
	pub, err := crypto.SigToPub(digest[:], sig)
	if err != nil {
		return common.Address{}, errors.New("unable to recover administrator signature")
	}
	address := crypto.PubkeyToAddress(*pub)
	if address != signature.Signer {
		return common.Address{}, errors.New("claimed administrator address differs from recovered signature")
	}
	return address, nil
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("choose prepare, inspect, sign, or assemble; this tool is offline and never broadcasts")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	statePath := flags.String("state", "", "saved eth_forgeRegistry result or JSON-RPC response")
	changePath := flags.String("change", "", "canonical RegistryChange JSON")
	requestPath := flags.String("request", "", "public approval request JSON")
	outputPath := flags.String("out", "", "new output file")
	chainText := flags.String("chain-id", "", "expected decimal RGN chain ID")
	genesisText := flags.String("genesis", "", "expected RGN genesis hash")
	preview := flags.Uint64("preview-height", 0, "preview inclusion height; not part of the signed digest")
	keyPath := flags.String("key-file", "", "local private key file; never output")
	approved := flags.String("approve-digest", "", "exact reviewed operation or acceptance digest")
	accept := flags.Bool("accept", false, "prove possession for the incoming administrator set")
	var signaturePaths stringFlags
	flags.Var(&signaturePaths, "signature", "public signature file; repeat for each signer")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return errors.New("invalid options; use prepare/inspect/sign/assemble with named file flags")
	}
	switch args[0] {
	case "prepare":
		if *statePath == "" || *changePath == "" || *chainText == "" || *genesisText == "" || *outputPath == "" {
			return errors.New("prepare requires --state, --change, --chain-id, --genesis, and --out")
		}
		state, err := loadState(*statePath)
		if err != nil {
			return err
		}
		snapshot, ctx, parentHeight, activation, err := state.Decode()
		if err != nil {
			return err
		}
		expected, ok := new(big.Int).SetString(*chainText, 10)
		if !ok || expected.Sign() <= 0 || expected.Cmp(ctx.ChainID) != 0 {
			return errors.New("saved state does not match the expected RGN chain ID")
		}
		genesis, err := parseHash(*genesisText)
		if err != nil || genesis != ctx.Genesis {
			return errors.New("saved state does not match the expected RGN genesis")
		}
		var change forgepool.RegistryChange
		if err = readJSON(*changePath, &change); err != nil {
			return err
		}
		if change.Sequence == 0 {
			if snapshot.Admin.Sequence == math.MaxUint64 {
				return errors.New("registry operation sequence exhausted")
			}
			change.Sequence = snapshot.Admin.Sequence + 1
		}
		height := *preview
		if height == 0 {
			if parentHeight == math.MaxUint64 {
				return errors.New("parent height exhausted")
			}
			height = parentHeight + 1
			if height < activation {
				height = activation
			}
			if snapshot.Admin.LastOperationHeight != 0 {
				if snapshot.Admin.LastOperationHeight > math.MaxUint64-ctx.MinOperationInterval {
					return errors.New("registry rate-limit arithmetic exhausted")
				}
				eligible := snapshot.Admin.LastOperationHeight + ctx.MinOperationInterval
				if height < eligible {
					height = eligible
				}
			}
		}
		request := approvalRequest{Version: 1, State: state, Change: change, PreviewHeight: height}
		request.Digest, err = forgepool.RegistryOperationDigest(state.Root, height, change, ctx)
		if err != nil {
			return err
		}
		if change.Kind == forgepool.RegistryRotateAdmins {
			request.AcceptanceDigest, err = forgepool.RegistryAcceptanceDigest(state.Root, height, change, ctx)
			if err != nil {
				return err
			}
		}
		if _, _, _, err = validateRequest(&request); err != nil {
			return err
		}
		if err = writeJSON(*outputPath, &request); err != nil {
			return err
		}
		return printReview(out, &request)
	case "inspect", "sign", "assemble":
		if *requestPath == "" {
			return errors.New("a --request public JSON file is required")
		}
		var request approvalRequest
		if err := readJSON(*requestPath, &request); err != nil {
			return err
		}
		snapshot, ctx, after, err := validateRequest(&request)
		if err != nil {
			return err
		}
		if args[0] == "inspect" {
			return printReview(out, &request)
		}
		if *outputPath == "" {
			return errors.New("a new --out file is required")
		}
		if args[0] == "sign" {
			if *keyPath == "" || *approved == "" {
				return errors.New("sign requires --key-file and the exact reviewed --approve-digest")
			}
			digest := request.Digest
			keys, _ := activeAuthority(snapshot, request.PreviewHeight)
			if *accept {
				if request.Change.Kind != forgepool.RegistryRotateAdmins {
					return errors.New("--accept applies only to incoming administrator rotation")
				}
				digest = request.AcceptanceDigest
				keys = request.Change.Keys
			}
			reviewed, err := parseHash(*approved)
			if err != nil || reviewed != digest {
				return errors.New("reviewed approval digest differs from this request")
			}
			key, err := loadPrivateKey(*keyPath)
			if err != nil {
				return err
			}
			defer key.D.SetInt64(0)
			address := crypto.PubkeyToAddress(key.PublicKey)
			if contains(ctx.WorkSigners, address) || !contains(keys, address) {
				return errors.New("this key is not an authorized separate registry administrator for the selected role")
			}
			signature, err := crypto.Sign(digest[:], key)
			if err != nil {
				return errors.New("unable to sign administrator approval")
			}
			result := approvalSignature{Version: 1, ChainID: ctx.ChainID.String(), Genesis: ctx.Genesis, ParentRoot: request.State.Root, Digest: digest, Signer: address, Acceptance: *accept, Signature: signature}
			if err = writeJSON(*outputPath, result); err != nil {
				return err
			}
			fmt.Fprintln(out, "Public administrator signature saved. Private key material was not included.")
			return nil
		}
		if len(signaturePaths) == 0 || len(signaturePaths) > 2*forgepool.RegistryMaxAdminKeys {
			return errors.New("assemble requires a bounded list of repeated --signature files")
		}
		keys, _ := activeAuthority(snapshot, request.PreviewHeight)
		type entry struct {
			address   common.Address
			signature []byte
		}
		var approvals, acceptances []entry
		seenApproval, seenAcceptance := map[common.Address]bool{}, map[common.Address]bool{}
		for _, path := range signaturePaths {
			var signature approvalSignature
			if err = readJSON(path, &signature); err != nil {
				return err
			}
			address, e := recoverSignature(signature, &request, ctx)
			if e != nil {
				return e
			}
			if signature.Acceptance {
				if !contains(request.Change.Keys, address) || seenAcceptance[address] {
					return errors.New("unauthorized or duplicate incoming administrator signature")
				}
				seenAcceptance[address] = true
				acceptances = append(acceptances, entry{address, signature.Signature})
			} else {
				if contains(ctx.WorkSigners, address) || !contains(keys, address) || seenApproval[address] {
					return errors.New("unauthorized or duplicate administrator approval")
				}
				seenApproval[address] = true
				approvals = append(approvals, entry{address, signature.Signature})
			}
		}
		sortEntries := func(items []entry) [][]byte {
			sort.Slice(items, func(i, j int) bool { return bytes.Compare(items[i].address[:], items[j].address[:]) < 0 })
			var sigs [][]byte
			for _, item := range items {
				sigs = append(sigs, item.signature)
			}
			return sigs
		}
		root, err := forgepool.RegistrySnapshotRoot(after)
		if err != nil {
			return err
		}
		op := forgepool.RegistryOperation{Before: snapshot, Change: request.Change, ResultRoot: root, Signatures: sortEntries(approvals), Acceptance: sortEntries(acceptances)}
		envelope := forgepool.RegistryEnvelope{Root: root, Checkpoint: request.PreviewHeight, Operations: []forgepool.RegistryOperation{op}}
		for _, coin := range after.Coins {
			if coin.Core {
				member, e := forgepool.RegistryMembershipFor(after, coin.Slot)
				if e != nil {
					return e
				}
				envelope.Members = append(envelope.Members, member)
			}
		}
		sort.Slice(envelope.Members, func(i, j int) bool { return envelope.Members[i].Coin.ChainID < envelope.Members[j].Coin.ChainID })
		if _, err = forgepool.ValidateRegistryEnvelope(request.State.Root, request.PreviewHeight, &envelope, ctx); err != nil {
			return err
		}
		if err = writeJSON(*outputPath, op); err != nil {
			return err
		}
		return printReview(out, &request)
	default:
		return errors.New("unknown offline command; choose prepare, inspect, sign, or assemble")
	}
}
