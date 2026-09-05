package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/At0-m/PTrans/internal/auth"
	"github.com/At0-m/PTrans/internal/config"
)

func run() error {
	user := flag.String("user", "alice", "token subject")
	ttl := flag.Duration("ttl", time.Hour, "token lifetime, up to 24h")
	flag.Parse()
	a, err := auth.New(config.JWT())
	if err != nil {
		return err
	}
	token, err := a.Issue(*user, *ttl)
	if err != nil {
		return err
	}
	fmt.Println(token)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
