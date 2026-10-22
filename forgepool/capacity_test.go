package forgepool

import (
	"encoding/binary"
	"testing"
)

// Capacity measurement only. This does not assert mining acceptance.
func BenchmarkForgeScryptCapacity(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		header := make([]byte, 80)
		for n := uint32(0); pb.Next(); n++ {
			binary.LittleEndian.PutUint32(header[76:], n)
			if _, err := ScryptHash(header); err != nil {
				b.Fatal(err)
			}
		}
	})
}
