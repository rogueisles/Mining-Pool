package proxy

import (
	"strings"
	"testing"

	"github.com/HawyUkr/open-progpow-pool/forgepool"
	"github.com/HawyUkr/open-progpow-pool/rpc"
)

func forgeRuntimeModeFixture(t *testing.T) (
	*forgeRuntime,
	*rpc.OGGAuxBlock,
	forgepool.ParentTemplate,
	*forgepool.DogeCandidate,
) {
	t.Helper()

	now := uint32(1700000000)

	var dgbPrev forgepool.Hash
	var dogePrev forgepool.Hash

	for i := range dgbPrev {
		dgbPrev[i] = byte(i + 1)
		dogePrev[i] = byte(0x80 + i)
	}

	dgbTpl := forgepool.ParentTemplate{
		Version:       2,
		Previous:      dgbPrev,
		CoinbaseValue: 5000000000,
		CurTime:       now,
		Bits:          0x207fffff,
		Height:        2000,
	}

	dogeTpl := forgepool.ParentTemplate{
		Version:       4,
		Previous:      dogePrev,
		CoinbaseValue: 1000000000000,
		CurTime:       now,
		Bits:          0x207fffff,
		Height:        3000,
	}

	doge, err := forgepool.BuildDogeCandidate(
		dogeTpl,
		[]byte{0x52},
		77,
	)
	if err != nil {
		t.Fatal(err)
	}

	ogg := &rpc.OGGAuxBlock{
		WorkHash:            "0x" + strings.Repeat("11", 32),
		Timestamp:           "0x6553f100",
		TemplateMaxValidity: "0x12c",
	}

	rt := &forgeRuntime{
		dgbPayout:       []byte{0x51},
		dogePayout:      []byte{0x52},
		extraNonce1Size: 4,
		extraNonce2Size: 4,
	}

	return rt, ogg, dgbTpl, doge
}

func TestForgeV2RuntimeBuildsFullMode(t *testing.T) {
	rt, ogg, dgbTpl, doge := forgeRuntimeModeFixture(t)

	job, err := rt.buildV2RuntimeJob(
		forgepool.ExternalDGB|forgepool.ExternalDOGE,
		ogg,
		4000,
		1,
		nil,
		1,
		&dgbTpl,
		doge,
	)
	if err != nil {
		t.Fatal(err)
	}

	if job.ExternalMask != forgepool.ExternalMask {
		t.Fatalf("mask=%#x want %#x", job.ExternalMask, forgepool.ExternalMask)
	}
	if !job.Digibyte.DigibyteActive {
		t.Fatal("full mode did not use real Digibyte parent")
	}
	if job.Digibyte.DOGE == nil {
		t.Fatal("full mode omitted Dogecoin")
	}
	if job.Digibyte.Template.Previous != dgbTpl.Previous {
		t.Fatal("full mode did not preserve Digibyte parent tip")
	}
}

func TestForgeV2RuntimeBuildsDGBOnlyMode(t *testing.T) {
	rt, ogg, dgbTpl, doge := forgeRuntimeModeFixture(t)

	job, err := rt.buildV2RuntimeJob(
		forgepool.ExternalDGB,
		ogg,
		4000,
		1,
		nil,
		2,
		&dgbTpl,
		doge,
	)
	if err != nil {
		t.Fatal(err)
	}

	if job.ExternalMask != forgepool.ExternalDGB {
		t.Fatalf("mask=%#x want DGB-only", job.ExternalMask)
	}
	if !job.Digibyte.DigibyteActive {
		t.Fatal("DGB-only mode did not use real Digibyte parent")
	}
	if job.Digibyte.DOGE != nil {
		t.Fatal("DGB-only mode retained Dogecoin")
	}
}

func TestForgeV2RuntimeBuildsDOGEOnlyExternalMode(t *testing.T) {
	rt, ogg, _, doge := forgeRuntimeModeFixture(t)

	job, err := rt.buildV2RuntimeJob(
		forgepool.ExternalDOGE,
		ogg,
		4000,
		1,
		nil,
		3,
		nil,
		doge,
	)
	if err != nil {
		t.Fatal(err)
	}

	if job.ExternalMask != forgepool.ExternalDOGE {
		t.Fatalf("mask=%#x want DOGE-only external mode", job.ExternalMask)
	}
	if job.Digibyte.DigibyteActive {
		t.Fatal("DOGE-only external mode marked synthetic parent as Digibyte-active")
	}
	if job.Digibyte.DOGE == nil {
		t.Fatal("DOGE-only external mode omitted Dogecoin")
	}
	if job.Digibyte.Template.Previous != (forgepool.Hash{}) {
		t.Fatal("synthetic parent unexpectedly carries a Digibyte previous hash")
	}
	if job.Digibyte.Template.CoinbaseValue != 0 {
		t.Fatal("synthetic parent unexpectedly carries Digibyte revenue")
	}

	_, err = job.Digibyte.FullBlock(
		make([]byte, 4),
		make([]byte, 4),
		job.Digibyte.Template.CurTime,
		0,
	)
	if err == nil {
		t.Fatal("synthetic parent could be submitted as a Digibyte block")
	}
}

func TestForgeV2RuntimeRejectsZeroMask(t *testing.T) {
	rt, ogg, dgbTpl, doge := forgeRuntimeModeFixture(t)

	if _, err := rt.buildV2RuntimeJob(
		0,
		ogg,
		4000,
		1,
		nil,
		4,
		&dgbTpl,
		doge,
	); err == nil {
		t.Fatal("zero external mask was accepted")
	}
}
