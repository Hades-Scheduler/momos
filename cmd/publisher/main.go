// Command publisher is the publish step binary (plan.md §11.7): the universal
// reporter. It validates review.json, freshness-gates, posts the split
// summary/inline review and check run, and calls back to Momos.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Hades-Scheduler/momos/internal/publisher"
)

func main() {
	cfg := publisher.FromEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := cfg.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "publisher: run error:", err)
		os.Exit(1)
	}
	fmt.Println("publisher: done")
}
