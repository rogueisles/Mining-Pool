// forge-status reads the running pool's derived, local STATUS snapshot. It
// never opens the journal for writing or overrides an unresolved chain result.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/HawyUkr/open-progpow-pool/proxy"
)

func main() {
	dir := flag.String("journal", "", "absolute FORGE accounting journal directory")
	maxAge := flag.Duration("max-age", 15*time.Second, "maximum status age")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "--journal is required")
		os.Exit(2)
	}
	s, err := proxy.ReadForgeAccountingStatus(*dir, time.Now(), *maxAge)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := json.NewEncoder(os.Stdout).Encode(s); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if s.Health != "healthy" {
		os.Exit(1)
	}
}
