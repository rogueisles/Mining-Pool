package forgepool

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestV2ExternalPolicyRejectsOGGOnly(t *testing.T) {
	tmpl := ExternalTemplate{
		Version:   OGGTemplateVersionV2Base,
		ValidFrom: 100,
		Expires:   200,
	}

	if _, err := TemplateExternalMask(tmpl); err == nil {
		t.Fatal("OGG-only V2 template accepted")
	}
}

func TestV2ExternalPolicyAllowsDGBOnly(t *testing.T) {
	tmpl := ExternalTemplate{
		Version:     OGGTemplateVersionV2Base | ExternalDGB,
		DGBPrevHash: common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		DGBBits:     0x1d00ffff,
		ValidFrom:   100,
		Expires:     200,
	}

	mask, err := TemplateExternalMask(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if mask != ExternalDGB {
		t.Fatalf("mask=%#x want %#x", mask, ExternalDGB)
	}
}

func TestV2ExternalPolicyAllowsDOGEOnly(t *testing.T) {
	tmpl := ExternalTemplate{
		Version:     OGGTemplateVersionV2Base | ExternalDOGE,
		DogeAuxHash: common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		ValidFrom:   100,
		Expires:     200,
	}

	mask, err := TemplateExternalMask(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if mask != ExternalDOGE {
		t.Fatalf("mask=%#x want %#x", mask, ExternalDOGE)
	}
}

func TestV2ExternalPolicyAllowsBoth(t *testing.T) {
	tmpl := ExternalTemplate{
		Version:     OGGTemplateVersionV2Base | ExternalDGB | ExternalDOGE,
		DGBPrevHash: common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		DGBBits:     0x1d00ffff,
		DogeAuxHash: common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		ValidFrom:   100,
		Expires:     200,
	}

	mask, err := TemplateExternalMask(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if mask != ExternalMask {
		t.Fatalf("mask=%#x want %#x", mask, ExternalMask)
	}
}
