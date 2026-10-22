package forgepool

import (
	"encoding/hex"
	"math/big"
	"testing"
)

func TestDigibyteScryptBlock29255Vector(t *testing.T) {
	headerHex := "01000000f615f7ce3b4fc6b8f61e8f89aedb1d0852507650533a9e3b10b9bbcc30639f279fcaa86746e1ef52d3edb3c4ad8259920d509bd073605c9bf1d59983752a6b06b817bb4ea78e011d012d59d4"
	header, err := hex.DecodeString(headerHex)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ScryptHash(header)
	if err != nil {
		t.Fatal(err)
	}
	want := "0000000110c8357966576df46f3b802ca897deb7ad18b12f1c24ecff6386ebd9"
	if got := DisplayHash(raw); got != want {
		t.Fatalf("scrypt mismatch: have %s want %s", got, want)
	}
	ok, err := MeetsDifficulty(raw, big.NewInt(1))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("block 29255 unexpectedly satisfies canonical diff1 target")
	}
}

func TestScryptRejectsNon80ByteHeader(t *testing.T) {
	if _, err := ScryptHash(make([]byte, 79)); err == nil {
		t.Fatal("accepted invalid Scrypt header length")
	}
}

func TestCompactTargetDiffOne(t *testing.T) {
	target, err := CompactTarget(0x1d00ffff)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := new(big.Int).SetString("00000000ffff0000000000000000000000000000000000000000000000000000", 16)
	if target.Cmp(want) != 0 {
		t.Fatalf("target mismatch have %x want %x", target, want)
	}
}

func TestDisplayHashRoundTrip(t *testing.T) {
	const display = "0000000110c8357966576df46f3b802ca897deb7ad18b12f1c24ecff6386ebd9"
	raw, err := ParseDisplayHash(display)
	if err != nil {
		t.Fatal(err)
	}
	if got := DisplayHash(raw); got != display {
		t.Fatalf("round trip mismatch: %s", got)
	}
}
