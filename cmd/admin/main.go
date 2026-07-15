package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/exitmesh/skills/internal/auth"
	"github.com/exitmesh/skills/internal/config"
	"github.com/exitmesh/skills/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "admin:", err)
		os.Exit(1)
	}
}
func run() error {
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "create-api-key" || args[0] == "api-key") {
		args = args[1:]
		if len(args) > 0 && args[0] == "create" {
			args = args[1:]
		}
	}
	flags := flag.NewFlagSet("admin", flag.ContinueOnError)
	name := flags.String("name", "", "human-readable API key name")
	validFor := flags.Duration("valid-for", 30*24*time.Hour, "API key validity, for example 720h")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("--name is required")
	}
	if *validFor <= 0 {
		return fmt.Errorf("--valid-for must be positive")
	}
	cfg, err := config.LoadAdmin()
	if err != nil {
		return err
	}
	keys, err := auth.NewKeyManager(cfg.EncryptionKey)
	if err != nil {
		return err
	}
	expires := time.Now().UTC().Add(*validFor)
	token, record, err := keys.Generate(*name, expires)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return err
	}
	id, err := db.CreateAPIKey(ctx, record)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"id": id, "name": *name, "expiresAt": expires, "token": token})
}
