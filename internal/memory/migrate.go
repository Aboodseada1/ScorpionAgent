package memory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// MigrateFromPython pulls data from the three legacy SQLite DBs into the unified app DB.
// Safe to call repeatedly (it uses INSERT OR IGNORE by ID).
func (d *DB) MigrateFromPython() error {
	calls := filepath.Join(d.Dir, "calls", "calls.sqlite3")
	intel := filepath.Join(d.Dir, "intel", "intel.sqlite3")

	if _, err := os.Stat(calls); err == nil {
		if err := d.importCalls(calls); err != nil {
			slog.Warn("migrate calls", "err", err)
		}
	}
	if _, err := os.Stat(intel); err == nil {
		if err := d.importIntel(intel); err != nil {
			slog.Warn("migrate intel", "err", err)
		}
	}
	return nil
}

func (d *DB) importCalls(path string) error {
	src, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return err
	}
	defer src.Close()
	rows, err := src.Query(`SELECT call_id,title,created_at,ended_at,manifest_json FROM calls`)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var callID, title, manifest string
		var created float64
		var ended sql.NullFloat64
		if err := rows.Scan(&callID, &title, &created, &ended, &manifest); err != nil {
			return err
		}
		var end any
		if ended.Valid {
			end = ended.Float64
		}
		_, err := d.Conn.Exec(
			`INSERT OR IGNORE INTO conversations(id,client_id,started_at,ended_at,summary,audio_dir) VALUES(?,?,?,?,?,?)`,
			callID, nil, created, end, title, filepath.Join("calls", callID),
		)
		if err != nil {
			return err
		}

		// Pull turn list out of manifest_json if present.
		var manifestMap struct {
			Turns []struct {
				Idx     int    `json:"idx"`
				Speaker string `json:"speaker"`
				Text    string `json:"text"`
				StartMs int64  `json:"t_start_ms"`
				EndMs   int64  `json:"t_end_ms"`
				Wav     string `json:"wav_path"`
			} `json:"turns"`
		}
		if json.Unmarshal([]byte(manifest), &manifestMap) == nil {
			for _, t := range manifestMap.Turns {
				_, _ = d.Conn.Exec(
					`INSERT OR IGNORE INTO turns(id,conversation_id,idx,speaker,text,t_start_ms,t_end_ms,wav_path) VALUES(?,?,?,?,?,?,?,?)`,
					uuid.NewString(), callID, t.Idx, t.Speaker, t.Text, t.StartMs, t.EndMs, t.Wav,
				)
			}
		}
		n++
	}
	slog.Info("migrated conversations", "n", n)
	return rows.Err()
}

func (d *DB) importIntel(path string) error {
	src, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return err
	}
	defer src.Close()
	rows, err := src.Query(`SELECT fact_id,call_id,category,fact_text,created_at FROM learned_facts`)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var factID, callID, cat, text string
		var created float64
		if err := rows.Scan(&factID, &callID, &cat, &text, &created); err != nil {
			return err
		}
		day := time.Unix(int64(created), 0).UTC().Format("2006-01-02")
		_, err := d.Conn.Exec(
			`INSERT OR IGNORE INTO facts(id,client_id,conversation_id,day,subject,predicate,object,category,confidence,source_turn_id,created_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			factID, nil, nullStr(callID), day, "call:"+callID, "states", text, cat, 0.7, nil, created,
		)
		if err != nil {
			return err
		}
		n++
	}
	slog.Info("migrated facts", "n", n)
	return rows.Err()
}
