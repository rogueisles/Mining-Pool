package main

import "encoding/hex"

// Configuration validation already rejects malformed scripts. Keep the native
// address/script check on every participating SOQ view as well.
func mustDecodeScript(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}
