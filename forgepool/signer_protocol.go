package forgepool

import "github.com/ethereum/go-ethereum/common"

const MaxDogeProposalBytes = 1000000
const MaxSignRequestBytes = 4 * 1024 * 1024

// Candidate is untrusted proposal data, never a replacement for the signer's
// trusted chain source. A signer accepting it must validate the exact rebuilt
// block through its own daemon before signing. Nil retains exact-GBT mode.
type DogeSigningEvidence struct {
	JobNonce  uint32          `json:"jobNonce"`
	Time      uint32          `json:"time"`
	Candidate *ParentTemplate `json:"candidate,omitempty"`
}

type TemplateSignRequest struct {
	Domain   string               `json:"domain"`
	Digest   string               `json:"digest"`
	Template ExternalTemplate     `json:"template"`
	WorkHash *common.Hash         `json:"workHash,omitempty"`
	Doge     *DogeSigningEvidence `json:"doge"`
}

type TemplateSignResponse struct {
	Signer    string `json:"signer"`
	Signature string `json:"signature"`
}
