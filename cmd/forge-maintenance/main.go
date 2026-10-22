// Explicit local maintenance. This executable does not start mining or payouts.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/HawyUkr/open-progpow-pool/proxy"
	"github.com/HawyUkr/open-progpow-pool/storage"
)

func read(path string, out interface{}) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := ioutil.ReadAll(io.LimitReader(f, 2*1024*1024+1))
	if err != nil {
		return err
	}
	if len(b) > 2*1024*1024 {
		return errors.New("maintenance input too large")
	}
	return json.Unmarshal(b, out)
}

func run() error {
	mode := flag.String("mode", "", "compact-preview, compact-apply, reconcile-preview, reconcile-apply")
	config := flag.String("config", "", "pool configuration file")
	dir := flag.String("output", "", "absolute review directory (compaction apply also stores an audit here)")
	planFile := flag.String("plan", "", "reviewed plan JSON file")
	confirm := flag.String("confirm-digest", "", "exact reviewed plan digest required to apply")
	height := flag.Int64("height", 0, "dedup OGG height")
	bucket := flag.String("bucket", "", "two hexadecimal ID characters")
	id := flag.String("receipt", "", "uncertain receipt ID")
	chain := flag.String("chain", "", "DGB, DOGE or OGG")
	hint := flag.String("block-hint", "", "optional OGG block hash; must be verified by original node")
	actor := flag.String("operator", "", "operator identity for reconciliation audit")
	reason := flag.String("reason", "", "single-line reconciliation reason")
	flag.Parse()
	var cfg proxy.Config
	if err := read(*config, &cfg); err != nil {
		return err
	}
	if *mode == "compact-preview" || *mode == "compact-apply" {
		client := storage.NewRedisClient(&cfg.Redis, cfg.Coin)
		defer client.Client().Close()
		var plan *storage.ForgeDedupPlan
		if *mode == "compact-preview" {
			var err error
			plan, err = client.PlanForgeDedup(*height, *bucket)
			if err != nil {
				return err
			}
			if err := proxy.SaveForgeReview(*dir, plan.Filename(), plan); err != nil {
				return err
			}
		} else {
			plan = &storage.ForgeDedupPlan{}
			if err := read(*planFile, plan); err != nil {
				return err
			}
			if err := plan.Validate(); err != nil {
				return err
			}
			if *confirm != plan.Digest {
				return errors.New("reviewed compaction digest required")
			}
			if err := proxy.SaveForgeReview(*dir, "apply-"+plan.Filename(), plan); err != nil {
				return err
			}
			if _, err := client.ApplyForgeDedup(*plan); err != nil {
				return err
			}
		}
		name := plan.Filename()
		if *mode == "compact-apply" {
			name = "apply-" + name
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{"digest": plan.Digest, "review": filepath.Join(*dir, name), "liveIDs": len(plan.LiveIDs), "compactIDs": len(plan.Packed) / 64, "mode": *mode})
	}
	if *mode == "reconcile-preview" {
		plan, err := proxy.PrepareForgeReconcile(&cfg, *id, *chain, *hint, *actor, *reason)
		if err != nil {
			return err
		}
		if err := proxy.SaveForgeReview(*dir, "reconcile-"+plan.Digest+".json", plan); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(plan)
	}
	if *mode == "reconcile-apply" {
		var plan proxy.ForgeReconcilePlan
		if err := read(*planFile, &plan); err != nil {
			return err
		}
		if err := proxy.ApplyForgeReconcile(&cfg, plan, *confirm); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"digest": plan.Digest, "result": "journal updated; restart pool for ordered replay"})
	}
	return errors.New("choose an explicit maintenance mode")
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
