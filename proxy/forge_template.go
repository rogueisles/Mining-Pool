package proxy

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// forgeSignedTemplate is fixed before a job is published. The work timestamp
// comes from the node's issued header; Created and the parent ntime cannot be
// substituted for it. This is pool policy, not a change to consensus encoding.
type forgeSignedTemplate struct {
	Multi       *forgepool.MultiTemplate
	Template    forgepool.ExternalTemplate
	Signatures  [][]byte
	WorkHash    string
	Timestamp   uint64
	MaxValidity uint64
}

func forgeNow(clock func() time.Time) time.Time {
	if clock != nil {
		return clock()
	}
	return time.Now()
}

func forgeWorkTime(work *rpc.OGGAuxBlock) (uint64, uint64, error) {
	if work == nil {
		return 0, 0, errors.New("missing issued OGG work")
	}
	stamp, err := hexutil.DecodeUint64(work.Timestamp)
	if err != nil || stamp == 0 || stamp > 1<<63-1 {
		return 0, 0, errors.New("node must provide the exact issued OGG timestamp")
	}
	limit, err := hexutil.DecodeUint64(work.TemplateMaxValidity)
	if err != nil || limit == 0 {
		return 0, 0, errors.New("node must provide templateMaxValidity")
	}
	return stamp, limit, nil
}

func (t *forgeSignedTemplate) fresh(now time.Time) bool {
	return t != nil && now.Unix() >= 0 && uint64(now.Unix()) >= t.Template.ValidFrom && uint64(now.Unix()) < t.Template.Expires
}

func (t *forgeSignedTemplate) validate(job *forgeJob, authorized []common.Address, quorum uint64, now time.Time) error {
	if t == nil {
		return errors.New("OGG job has no signed template")
	}
	if job == nil || job.Digibyte == nil {
		return errors.New("missing FORGE parent job")
	}

	stamp, limit, err := forgeWorkTime(job.OGG)
	if err != nil {
		return err
	}
	if !strings.EqualFold(t.WorkHash, job.OGG.WorkHash) || t.Timestamp != stamp || t.MaxValidity != limit {
		return errors.New("signed template does not match issued OGG work")
	}

	if t.Template.Expires <= stamp || t.Template.Expires-stamp > limit {
		return errors.New("signed template fields do not match issued job")
	}

	if t.Multi != nil {
		if !job.ProtocolV3 || !forgepool.RevenueAnchorPresent(t.Multi.Mask) || !t.fresh(now) || t.Multi.Mask != job.ExternalMask || t.Multi.ValidFrom != stamp || t.Multi.Expires != t.Template.Expires {
			return errors.New("multi-chain issued work mismatch")
		}
		rebuilt, e := job.Digibyte.MultiTemplate(stamp, t.Multi.Expires)
		if e != nil || !forgepool.MultiTemplatesEqual(rebuilt, *t.Multi) {
			return errors.New("multi-chain parent body or treasury mismatch")
		}
		if t.Multi.Version >= 4 && job.Signed != nil {
			if e := job.Digibyte.CheckCertificate(*t.Multi, t.Signatures); e != nil {
				return e
			}
		}
		work := common.HexToHash(t.WorkHash)
		if t.Multi.Version == 5 {
			if job.Registry == nil || job.RegistryContext == nil {
				return errors.New("missing registry validation context")
			}
			var e error
			work, e = forgepool.FlexibleSigningWork(work, job.Registry)
			if e != nil {
				return e
			}
		}
		return forgepool.ValidateMultiSignatures(*t.Multi, work, t.Signatures, authorized, quorum, stamp)
	}
	if job.ProtocolV3 {
		return errors.New("missing multi-chain authorization")
	}
	if t.Template.Version == forgepool.OGGTemplateVersion {
		// Exact legacy V1 behavior.
		if !job.Digibyte.DigibyteActive || job.Digibyte.DOGE == nil {
			return errors.New("missing legacy merged job")
		}
		prev := forgepoolReverse(job.Digibyte.Template.Previous)
		want := forgepool.ExternalTemplate{
			Version:     forgepool.OGGTemplateVersion,
			DGBPrevHash: common.BytesToHash(prev[:]),
			DGBBits:     job.Digibyte.Template.Bits,
			DogeAuxHash: common.HexToHash("0x" + job.Digibyte.DOGE.DisplayHash),
			ValidFrom:   stamp,
			Expires:     t.Template.Expires,
		}
		if t.Template != want {
			return errors.New("signed template fields do not match issued job")
		}
	} else {
		mask, err := forgepool.TemplateExternalMask(t.Template)
		if err != nil {
			return err
		}
		if job.ExternalMask == 0 || mask != job.ExternalMask {
			return errors.New("signed V2 participation mask does not match issued job")
		}

		dgbActive := mask&forgepool.ExternalDGB != 0
		dogeActive := mask&forgepool.ExternalDOGE != 0

		if job.Digibyte.DigibyteActive != dgbActive {
			return errors.New("signed Digibyte participation does not match parent job")
		}
		if (job.Digibyte.DOGE != nil) != dogeActive {
			return errors.New("signed Dogecoin participation does not match parent job")
		}

		want := forgepool.ExternalTemplate{
			Version:   forgepool.OGGTemplateVersionV2Base | mask,
			ValidFrom: stamp,
			Expires:   t.Template.Expires,
		}

		if dgbActive {
			prev := forgepoolReverse(job.Digibyte.Template.Previous)
			want.DGBPrevHash = common.BytesToHash(prev[:])
			want.DGBBits = job.Digibyte.Template.Bits
		}
		if dogeActive {
			want.DogeAuxHash = common.HexToHash("0x" + job.Digibyte.DOGE.DisplayHash)
		}

		if t.Template != want {
			return errors.New("signed V2 template fields do not match issued job")
		}
	}

	if !t.fresh(now) {
		return errors.New("signed OGG template expired or not yet valid")
	}

	// Verify against the node's exact issued timestamp.
	if t.Template.Version&^forgepool.ExternalMask == forgepool.OGGTemplateVersionV2Base {
		raw := strings.TrimPrefix(strings.TrimSpace(t.WorkHash), "0x")
		if len(raw) != 64 {
			return errors.New("invalid exact OGG WorkHash")
		}
		workHash := common.HexToHash("0x" + raw)
		if workHash == (common.Hash{}) {
			return errors.New("zero OGG WorkHash")
		}
		return forgepool.ValidateTemplateSignaturesForWork(
			t.Template,
			workHash,
			t.Signatures,
			authorized,
			quorum,
			time.Unix(int64(stamp), 0),
		)
	}

	return forgepool.ValidateTemplateSignatures(
		t.Template,
		t.Signatures,
		authorized,
		quorum,
		time.Unix(int64(stamp), 0),
	)
}

func (rt *forgeRuntime) signJob(job *forgeJob) error {
	if job == nil || job.Digibyte == nil {
		return errors.New("missing FORGE signing job")
	}
	job.Signed = nil
	if job.ProtocolV3 {
		return rt.signMultiJob(job)
	}

	if rt.templateQuorum == 0 ||
		rt.templateQuorum > uint64(len(rt.signers)) ||
		rt.templateQuorum > uint64(len(rt.authorized)) {
		return errors.New("invalid external signer quorum")
	}

	stamp, limit, err := forgeWorkTime(job.OGG)
	if err != nil {
		return err
	}

	seconds := int64(rt.templateValidity / time.Second)
	if seconds <= 0 {
		return errors.New("invalid configured template validity")
	}
	span := uint64(seconds)
	if span > limit {
		span = limit
	}
	if stamp > uint64(1<<63-1)-span {
		return errors.New("template timestamp overflow")
	}
	expires := stamp + span

	var (
		tmpl     forgepool.ExternalTemplate
		evidence *forgepool.DogeSigningEvidence
		workHash *common.Hash
	)

	if job.ExternalMask == 0 {
		// Preserve exact V1 behavior for existing fixtures/runtime until D2B
		// explicitly publishes V2 jobs.
		if !job.Digibyte.DigibyteActive || job.Digibyte.DOGE == nil {
			return errors.New("missing legacy merged signing job")
		}

		prev := forgepoolReverse(job.Digibyte.Template.Previous)
		tmpl = forgepool.ExternalTemplate{
			Version:     forgepool.OGGTemplateVersion,
			DGBPrevHash: common.BytesToHash(prev[:]),
			DGBBits:     job.Digibyte.Template.Bits,
			DogeAuxHash: common.HexToHash("0x" + job.Digibyte.DOGE.DisplayHash),
			ValidFrom:   stamp,
			Expires:     expires,
		}
		evidence = &forgepool.DogeSigningEvidence{
			JobNonce:  job.Digibyte.DOGE.JobNonce,
			Time:      job.Digibyte.DOGE.Template.CurTime,
			Candidate: &job.Digibyte.DOGE.Template,
		}
	} else {
		mask := job.ExternalMask

		// Pool policy and node consensus both forbid V2 mask zero.
		if mask&^forgepool.ExternalMask != 0 {
			return fmt.Errorf("invalid V2 external mask %#x", mask)
		}

		dgbActive := mask&forgepool.ExternalDGB != 0
		dogeActive := mask&forgepool.ExternalDOGE != 0

		if job.Digibyte.DigibyteActive != dgbActive {
			return errors.New("V2 Digibyte participation does not match parent job")
		}
		if (job.Digibyte.DOGE != nil) != dogeActive {
			return errors.New("V2 Dogecoin participation does not match parent job")
		}

		tmpl = forgepool.ExternalTemplate{
			Version:   forgepool.OGGTemplateVersionV2Base | mask,
			ValidFrom: stamp,
			Expires:   expires,
		}

		if dgbActive {
			prev := forgepoolReverse(job.Digibyte.Template.Previous)
			tmpl.DGBPrevHash = common.BytesToHash(prev[:])
			tmpl.DGBBits = job.Digibyte.Template.Bits
		}
		if dogeActive {
			tmpl.DogeAuxHash = common.HexToHash("0x" + job.Digibyte.DOGE.DisplayHash)
			evidence = &forgepool.DogeSigningEvidence{
				JobNonce:  job.Digibyte.DOGE.JobNonce,
				Time:      job.Digibyte.DOGE.Template.CurTime,
				Candidate: &job.Digibyte.DOGE.Template,
			}
		}

		if _, err := forgepool.TemplateExternalMask(tmpl); err != nil {
			return err
		}

		raw := strings.TrimPrefix(strings.TrimSpace(job.OGG.WorkHash), "0x")
		if len(raw) != 64 {
			return errors.New("invalid exact OGG WorkHash")
		}
		bound := common.HexToHash("0x" + raw)
		if bound == (common.Hash{}) {
			return errors.New("zero OGG WorkHash")
		}
		workHash = &bound
	}

	signed := &forgeSignedTemplate{
		WorkHash:    job.OGG.WorkHash,
		Timestamp:   stamp,
		MaxValidity: limit,
		Template:    tmpl,
	}

	if !signed.fresh(forgeNow(rt.clock)) {
		return errors.New("issued OGG work cannot support a fresh signed template")
	}

	var failures []string
	for _, signer := range rt.signers {
		if !signed.fresh(forgeNow(rt.clock)) {
			return errors.New("OGG template expired while gathering signatures")
		}

		sig, err := signer.sign(
			signed.Template,
			evidence,
			workHash,
		)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", signer.name, err))
			continue
		}

		signed.Signatures = append(signed.Signatures, sig)
		if uint64(len(signed.Signatures)) >= rt.templateQuorum {
			break
		}
	}

	if err := signed.validate(
		job,
		rt.authorized,
		rt.templateQuorum,
		forgeNow(rt.clock),
	); err != nil {
		return fmt.Errorf("%w (signer errors: %s)", err, strings.Join(failures, "; "))
	}

	job.Signed = signed
	return nil
}
