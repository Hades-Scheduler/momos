// Command reviewer is the review step binary (plan.md §11.3). It reads its
// configuration from the step environment, computes the diff, runs the review
// (oneshot or agentic), and writes /shared/out/review.json. It holds no forge
// credentials. A non-zero exit is tolerated by the job (continue_on_error), and
// the publisher reports the missing result.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Hades-Scheduler/momos/internal/reviewer"
)

func main() {
	cfg, err := reviewer.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "reviewer: config error:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := cfg.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "reviewer: run error:", err)
		os.Exit(1)
	}
	fmt.Println("reviewer: wrote review.json")
}
