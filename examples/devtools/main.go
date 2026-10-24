// Command devtools runs a counter with a live local inspector. The session URL
// is printed for this development process only. Stop with Ctrl+C.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/yldm-tech/nodra"
	"github.com/yldm-tech/nodra/devtools"
)

type counter struct {
	Count int `json:"count"`
}

func main() {
	interval := flag.Duration("interval", time.Second, "interval between demo actions")
	allowRestore := flag.Bool("restore", false, "enable JSON editing and snapshot restoration")
	flag.Parse()
	if *interval <= 0 {
		log.Fatal("interval must be positive")
	}
	store := nodra.Define("counter", func() counter { return counter{} }).New()
	inspector := devtools.New(devtools.Options{AllowRestore: *allowRestore})
	defer func() { _ = inspector.Close() }()
	if _, err := devtools.Attach(inspector, "counter", store); err != nil {
		log.Fatal(err)
	}
	increment := func(v *counter) error { v.Count++; return nil }
	_ = store.Do("increment", increment)
	_ = store.Do("rejected example", func(*counter) error { return errors.New("example validation failed") })
	url, err := inspector.Listen()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(url)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := store.Do("increment", increment); err != nil {
				log.Print(err)
			}
		}
	}
}
