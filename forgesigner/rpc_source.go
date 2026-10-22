package forgesigner

import (
	"context"
	"errors"

	"github.com/HawyUkr/open-progpow-pool/rpc"
)

// RPCSource uses signer-owned clients with finite HTTP timeouts. Operators
// must pin these to independently trusted, synchronized full nodes and networks.
// Daemon consensus is the trust boundary, not caller-supplied block data.
type RPCSource struct {
	Digibyte *rpc.RPCClient
	Dogecoin *rpc.RPCClient
}

var _ Source = RPCSource{}
var _ DigibyteSource = RPCSource{}
var _ DogecoinSource = RPCSource{}
var _ ProposalSource = RPCSource{}

func (s RPCSource) ValidateDogeProposal(ctx context.Context, block []byte) error {
	if s.Dogecoin == nil {
		return errors.New("missing trusted DOGE RPC")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Dogecoin.ValidateDogecoinProposal(ctx, block)
}

// DigibyteTemplate obtains only Digibyte trusted state. A DOGE outage must not
// prevent a V2 DGB-only template from being independently attested.
func (s RPCSource) DigibyteTemplate(ctx context.Context) (*rpc.BlockTemplateReply, error) {
	if s.Digibyte == nil {
		return nil, errors.New("missing trusted Digibyte RPC")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	out, err := s.Digibyte.GetDigibyteBlockTemplate()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// DogecoinTemplate obtains only Dogecoin trusted state. An DGB outage must not
// prevent a V2 DOGE-only template from being independently attested.
func (s RPCSource) DogecoinTemplate(ctx context.Context) (*rpc.BlockTemplateReply, error) {
	if s.Dogecoin == nil {
		return nil, errors.New("missing trusted DOGE RPC")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	out, err := s.Dogecoin.GetDogecoinBlockTemplate()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Templates preserves the exact legacy V1 contract: both external chains must
// be independently available.
func (s RPCSource) Templates(ctx context.Context) (*rpc.BlockTemplateReply, *rpc.BlockTemplateReply, error) {
	dgb, err := s.DigibyteTemplate(ctx)
	if err != nil {
		return nil, nil, err
	}

	doge, err := s.DogecoinTemplate(ctx)
	if err != nil {
		return nil, nil, err
	}

	return dgb, doge, ctx.Err()
}
