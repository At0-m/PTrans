package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/At0-m/PTrans/internal/config"
	"github.com/At0-m/PTrans/internal/domain"
	"github.com/At0-m/PTrans/internal/refunds"
	postgres "github.com/At0-m/PTrans/internal/storage"
)

func run() error {
	user := flag.String("user", "alice", "demo payment owner")
	amount := flag.Int64("amount", 10000, "demo payment amount in minor units")
	flag.Parse()
	if os.Getenv("ALLOW_DEMO_SEED") != "true" {
		return errors.New("set ALLOW_DEMO_SEED=true for the local lab")
	}
	if *amount <= 0 || *user == "" {
		return errors.New("invalid user or amount")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := postgres.New(ctx, config.Env("DATABASE_URL", "postgres://postgres:postgres@localhost:5433/ptrans?sslmode=disable"))
	if err != nil {
		return err
	}
	defer store.Close()
	id, err := refunds.NewID("pay")
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	payment, _, err := store.CreatePayment(ctx, *user, domain.Payment{ID: id, Amount: *amount, Currency: "RUB", Status: domain.PaymentSucceeded, CreatedAt: now, UpdatedAt: now}, "")
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(payment)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
