// Command migrate one-shot imports the legacy Python SQLite databases
// (data/calls/calls.sqlite3, data/intel/intel.sqlite3) into the new unified
// data/app.sqlite3. Safe to re-run.
package main

import (
	"flag"
	"log/slog"
	"os"

	"scorpion/agent/internal/memory"
)

func main() {
	dir := flag.String("dir", "./data", "data directory")
	flag.Parse()
	db, err := memory.Open(*dir)
	if err != nil {
		slog.Error("open", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.MigrateFromPython(); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}
	slog.Info("migration complete")
}
