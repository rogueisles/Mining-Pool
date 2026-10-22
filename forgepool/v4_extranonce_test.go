package forgepool

import (
	"bytes"
	"errors"
	"testing"
)

func parseV4DirectPushes(script []byte) ([][]byte, error) {
	var values [][]byte
	for len(script) != 0 {
		length := int(script[0])
		script = script[1:]
		if length > 75 || length > len(script) {
			return nil, errors.New("script contains an opcode or an incomplete push")
		}
		values = append(values, script[:length])
		script = script[length:]
	}
	return values, nil
}

func TestV4ExtranonceHasFixedNativeScriptBoundaries(t *testing.T) {
	for _, auxiliary := range [][]byte{nil, {0x2c}, {0xac, 0xae, 0x4d, 0xff, 0xff}, {0x08, 0x4c, 0x4d, 0x4e}} {
		template := ParentTemplate{Height: 300001, CoinbaseValue: 100000000,
			CoinbaseAux: map[string][]byte{"flags": auxiliary}}
		commitment := append(append([]byte(nil), MergedMiningMagic...), bytes.Repeat([]byte{0xac}, 40)...)
		plan, err := buildV4CoinbasePlan(template, commitment, []byte{0x51}, 4, 4)
		if err != nil {
			t.Fatal(err)
		}
		for value := 0; value < 256; value++ {
			for position := 0; position < 9; position++ {
				extra := make([]byte, 8)
				if position == 8 {
					for i := range extra {
						extra[i] = byte(value)
					}
				} else {
					extra[position] = byte(value)
				}
				base, err := plan.BaseCoinbase(extra[:4], extra[4:])
				if err != nil {
					t.Fatal(err)
				}
				length := int(base[41])
				script := base[42 : 42+length]
				parsed, err := parseV4DirectPushes(script)
				if len(script) > 100 || err != nil || len(parsed) < 3 || !bytes.Equal(parsed[len(parsed)-2], commitment) || !bytes.Equal(parsed[len(parsed)-1], extra) {
					t.Fatal("extranonce is not the final complete data push", err)
				}
				if !bytes.Equal(base[len(plan.Coinb1):len(plan.Coinb1)+8], extra) {
					t.Fatal("serialized extranonce offset differs from Stratum fragments")
				}
			}
		}
	}
}

func TestV4CoinbaseScriptExactSizeAndFixedExtranonce(t *testing.T) {
	commitment := append(append([]byte(nil), MergedMiningMagic...), make([]byte, 40)...)
	template := ParentTemplate{Height: 300001, CoinbaseValue: 1,
		CoinbaseAux: map[string][]byte{"flags": bytes.Repeat([]byte{0xac}, 41)}}
	plan, err := buildV4CoinbasePlan(template, commitment, []byte{0x51}, 4, 4)
	if err != nil || len(plan.ScriptPrefix)+8 != 100 {
		t.Fatal("exactly 100-byte script rejected", err)
	}
	template.CoinbaseAux["flags"] = bytes.Repeat([]byte{0xac}, 42)
	if _, err := buildV4CoinbasePlan(template, commitment, []byte{0x51}, 4, 4); err == nil {
		t.Fatal("oversized script accepted")
	}
	for _, sizes := range [][2]int{{0, 8}, {8, 0}, {3, 5}, {4, 5}, {-1, 9}} {
		if _, err := buildV4CoinbasePlan(ParentTemplate{Height: 1}, commitment, []byte{0x51}, sizes[0], sizes[1]); err == nil {
			t.Fatal("noncanonical extranonce dimensions accepted", sizes)
		}
	}
}
