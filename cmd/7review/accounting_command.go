package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Y4NN777/7review/agent/accounting"
)

func runAccountingCommand(args []string, out io.Writer) error {
	if len(args) != 1 || args[0] != "migrate" {
		return fmt.Errorf("usage: 7review accounting migrate (uses ACCOUNTING_DATABASE_URL)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := accounting.OpenDatabase(ctx, os.Getenv("ACCOUNTING_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	if err = accounting.Migrate(ctx, db); err != nil {
		return fmt.Errorf("accounting migration failed: %w", err)
	}
	_, err = fmt.Fprintln(out, "Accounting schema is ready; no model routes were authorized.")
	return err
}
