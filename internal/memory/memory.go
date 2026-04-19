// Package memory owns the unified SQLite store (clients, convos, turns, facts, actions, embeddings).
package memory

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

type DB struct {
	Conn *sql.DB
	Dir  string
}

func Open(dataDir string) (*DB, error) {
	dsn := filepath.Join(dataDir, "app.sqlite3") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	if _, err := conn.Exec(schemaSQL); err != nil {
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &DB{Conn: conn, Dir: dataDir}, nil
}

func (d *DB) Close() error { return d.Conn.Close() }

// ---------- Clients ----------

type Client struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Business    string          `json:"business"`
	Industry    string          `json:"industry"`
	Stage       string          `json:"stage"`
	Role        string          `json:"role"`
	Notes       string          `json:"notes"`
	Profile     json.RawMessage `json:"profile,omitempty"`
	AvatarColor string          `json:"avatar_color"`
	CreatedAt   float64         `json:"created_at"`
	UpdatedAt   float64         `json:"updated_at"`
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func (d *DB) CreateClient(c *Client) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	if c.Stage == "" {
		c.Stage = "new"
	}
	if c.Role == "" {
		c.Role = "seller"
	}
	if len(c.Profile) == 0 {
		c.Profile = json.RawMessage(`{}`)
	}
	if c.AvatarColor == "" {
		c.AvatarColor = pickAvatarColor(c.Name)
	}
	c.CreatedAt = now()
	c.UpdatedAt = c.CreatedAt
	_, err := d.Conn.Exec(
		`INSERT INTO clients(id,name,business,industry,stage,role,notes,profile_json,avatar_color,created_at,updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Name, c.Business, c.Industry, c.Stage, c.Role, c.Notes, string(c.Profile), c.AvatarColor, c.CreatedAt, c.UpdatedAt,
	)
	return err
}

func (d *DB) UpdateClient(c *Client) error {
	c.UpdatedAt = now()
	if len(c.Profile) == 0 {
		c.Profile = json.RawMessage(`{}`)
	}
	_, err := d.Conn.Exec(
		`UPDATE clients SET name=?,business=?,industry=?,stage=?,role=?,notes=?,profile_json=?,avatar_color=?,updated_at=? WHERE id=?`,
		c.Name, c.Business, c.Industry, c.Stage, c.Role, c.Notes, string(c.Profile), c.AvatarColor, c.UpdatedAt, c.ID,
	)
	return err
}

func (d *DB) DeleteClient(id string) error {
	_, err := d.Conn.Exec(`DELETE FROM clients WHERE id=?`, id)
	return err
}

// ClearConversations nukes every conversation for a client. Cascades via FK to
// turns + facts + actions rows that were attached to those conversations.
// It also drops any per-conversation embeddings.
func (d *DB) ClearConversations(clientID string) (int64, error) {
	tx, err := d.Conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// Drop embeddings that reference conversations of this client (e.g. turn
	// embeddings if we ever add them). Safe even if none exist.
	_, _ = tx.Exec(
		`DELETE FROM embeddings WHERE kind IN ('turn','conversation') AND ref_id IN (SELECT id FROM conversations WHERE client_id=?)`,
		clientID,
	)
	res, err := tx.Exec(`DELETE FROM conversations WHERE client_id=?`, clientID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

// ClearFacts removes every learned fact for a client.
func (d *DB) ClearFacts(clientID string) (int64, error) {
	res, err := d.Conn.Exec(`DELETE FROM facts WHERE client_id=?`, clientID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ClearActions removes every logged action for a client.
func (d *DB) ClearActions(clientID string) (int64, error) {
	res, err := d.Conn.Exec(`DELETE FROM actions WHERE client_id=?`, clientID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ClearDocs removes every knowledge doc tied to a client, plus their embeddings.
func (d *DB) ClearDocs(clientID string) (int64, error) {
	tx, err := d.Conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	_, _ = tx.Exec(
		`DELETE FROM embeddings WHERE kind='doc' AND ref_id IN (SELECT id FROM client_docs WHERE client_id=?)`,
		clientID,
	)
	res, err := tx.Exec(`DELETE FROM client_docs WHERE client_id=?`, clientID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

func (d *DB) GetClient(id string) (*Client, error) {
	row := d.Conn.QueryRow(`SELECT id,name,business,industry,stage,role,notes,profile_json,avatar_color,created_at,updated_at FROM clients WHERE id=?`, id)
	return scanClient(row)
}

func (d *DB) ListClients() ([]*Client, error) {
	rows, err := d.Conn.Query(`SELECT id,name,business,industry,stage,role,notes,profile_json,avatar_color,created_at,updated_at FROM clients ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Client
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type rowScan interface {
	Scan(dest ...any) error
}

func scanClient(r rowScan) (*Client, error) {
	var c Client
	var prof string
	err := r.Scan(&c.ID, &c.Name, &c.Business, &c.Industry, &c.Stage, &c.Role, &c.Notes, &prof, &c.AvatarColor, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	c.Profile = json.RawMessage(prof)
	return &c, nil
}

// ---------- Conversations & Turns ----------

type Conversation struct {
	ID        string  `json:"id"`
	ClientID  string  `json:"client_id,omitempty"`
	StartedAt float64 `json:"started_at"`
	EndedAt   *float64 `json:"ended_at,omitempty"`
	Summary   string  `json:"summary"`
	AudioDir  string  `json:"audio_dir"`
}

type Turn struct {
	ID        string `json:"id"`
	ConvID    string `json:"conversation_id"`
	Idx       int    `json:"idx"`
	Speaker   string `json:"speaker"`
	Text      string `json:"text"`
	StartMs   int64  `json:"t_start_ms"`
	EndMs     int64  `json:"t_end_ms"`
	WavPath   string `json:"wav_path,omitempty"`
}

func (d *DB) CreateConversation(clientID string) (*Conversation, error) {
	c := &Conversation{ID: uuid.NewString(), ClientID: clientID, StartedAt: now()}
	c.AudioDir = filepath.Join("calls", c.ID)
	_, err := d.Conn.Exec(
		`INSERT INTO conversations(id,client_id,started_at,audio_dir) VALUES(?,?,?,?)`,
		c.ID, nullStr(clientID), c.StartedAt, c.AudioDir,
	)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (d *DB) EndConversation(id, summary string) error {
	t := now()
	_, err := d.Conn.Exec(`UPDATE conversations SET ended_at=?, summary=? WHERE id=?`, t, summary, id)
	return err
}

func (d *DB) GetConversation(id string) (*Conversation, error) {
	row := d.Conn.QueryRow(`SELECT id,COALESCE(client_id,''),started_at,ended_at,summary,audio_dir FROM conversations WHERE id=?`, id)
	var c Conversation
	var ended sql.NullFloat64
	err := row.Scan(&c.ID, &c.ClientID, &c.StartedAt, &ended, &c.Summary, &c.AudioDir)
	if err != nil {
		return nil, err
	}
	if ended.Valid {
		c.EndedAt = &ended.Float64
	}
	return &c, nil
}

func (d *DB) ListConversations(clientID string, limit int) ([]*Conversation, error) {
	q := `SELECT id,COALESCE(client_id,''),started_at,ended_at,summary,audio_dir FROM conversations`
	args := []any{}
	if clientID != "" {
		q += ` WHERE client_id=?`
		args = append(args, clientID)
	}
	q += ` ORDER BY started_at DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := d.Conn.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Conversation
	for rows.Next() {
		var c Conversation
		var ended sql.NullFloat64
		if err := rows.Scan(&c.ID, &c.ClientID, &c.StartedAt, &ended, &c.Summary, &c.AudioDir); err != nil {
			return nil, err
		}
		if ended.Valid {
			c.EndedAt = &ended.Float64
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

func (d *DB) AppendTurn(t *Turn) error {
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	if t.Idx == 0 {
		row := d.Conn.QueryRow(`SELECT COALESCE(MAX(idx),0)+1 FROM turns WHERE conversation_id=?`, t.ConvID)
		_ = row.Scan(&t.Idx)
	}
	_, err := d.Conn.Exec(
		`INSERT INTO turns(id,conversation_id,idx,speaker,text,t_start_ms,t_end_ms,wav_path) VALUES(?,?,?,?,?,?,?,?)`,
		t.ID, t.ConvID, t.Idx, t.Speaker, t.Text, t.StartMs, t.EndMs, t.WavPath,
	)
	return err
}

func (d *DB) ListTurns(convID string) ([]*Turn, error) {
	rows, err := d.Conn.Query(`SELECT id,conversation_id,idx,speaker,text,t_start_ms,t_end_ms,wav_path FROM turns WHERE conversation_id=? ORDER BY idx ASC`, convID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Turn
	for rows.Next() {
		var t Turn
		if err := rows.Scan(&t.ID, &t.ConvID, &t.Idx, &t.Speaker, &t.Text, &t.StartMs, &t.EndMs, &t.WavPath); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// ---------- Facts ----------

type Fact struct {
	ID          string  `json:"id"`
	ClientID    string  `json:"client_id,omitempty"`
	ConvID      string  `json:"conversation_id,omitempty"`
	Day         string  `json:"day"`
	Subject     string  `json:"subject"`
	Predicate   string  `json:"predicate"`
	Object      string  `json:"object"`
	Category    string  `json:"category"`
	Confidence  float64 `json:"confidence"`
	SourceTurn  string  `json:"source_turn_id,omitempty"`
	CreatedAt   float64 `json:"created_at"`
}

func (d *DB) InsertFact(f *Fact) error {
	if f.ID == "" {
		f.ID = uuid.NewString()
	}
	if f.Day == "" {
		f.Day = time.Now().UTC().Format("2006-01-02")
	}
	if f.Category == "" {
		f.Category = "general"
	}
	f.CreatedAt = now()
	_, err := d.Conn.Exec(
		`INSERT INTO facts(id,client_id,conversation_id,day,subject,predicate,object,category,confidence,source_turn_id,created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		f.ID, nullStr(f.ClientID), nullStr(f.ConvID), f.Day, f.Subject, f.Predicate, f.Object, f.Category, f.Confidence, nullStr(f.SourceTurn), f.CreatedAt,
	)
	return err
}

func (d *DB) ListFacts(clientID, convID string, limit int) ([]*Fact, error) {
	q := `SELECT id,COALESCE(client_id,''),COALESCE(conversation_id,''),day,subject,predicate,object,category,confidence,COALESCE(source_turn_id,''),created_at FROM facts`
	var where []string
	var args []any
	if clientID != "" {
		where = append(where, "client_id=?")
		args = append(args, clientID)
	}
	if convID != "" {
		where = append(where, "conversation_id=?")
		args = append(args, convID)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := d.Conn.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Fact
	for rows.Next() {
		var f Fact
		if err := rows.Scan(&f.ID, &f.ClientID, &f.ConvID, &f.Day, &f.Subject, &f.Predicate, &f.Object, &f.Category, &f.Confidence, &f.SourceTurn, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &f)
	}
	return out, rows.Err()
}

// ---------- Actions ----------

type Action struct {
	ID         string          `json:"id"`
	ClientID   string          `json:"client_id,omitempty"`
	ConvID     string          `json:"conversation_id,omitempty"`
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
	Status     string          `json:"status"`
	DueAt      *float64        `json:"due_at,omitempty"`
	CreatedAt  float64         `json:"created_at"`
	ExecutedAt *float64        `json:"executed_at,omitempty"`
}

func (d *DB) InsertAction(a *Action) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	if a.Status == "" {
		a.Status = "pending"
	}
	if len(a.Payload) == 0 {
		a.Payload = json.RawMessage(`{}`)
	}
	a.CreatedAt = now()
	var due any
	if a.DueAt != nil {
		due = *a.DueAt
	}
	_, err := d.Conn.Exec(
		`INSERT INTO actions(id,client_id,conversation_id,type,payload_json,status,due_at,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		a.ID, nullStr(a.ClientID), nullStr(a.ConvID), a.Type, string(a.Payload), a.Status, due, a.CreatedAt,
	)
	return err
}

func (d *DB) UpdateActionStatus(id, status string) error {
	var executed any
	if status == "done" || status == "completed" || status == "failed" {
		executed = now()
	}
	_, err := d.Conn.Exec(`UPDATE actions SET status=?, executed_at=? WHERE id=?`, status, executed, id)
	return err
}

func (d *DB) ListActions(clientID, convID string, limit int) ([]*Action, error) {
	q := `SELECT id,COALESCE(client_id,''),COALESCE(conversation_id,''),type,payload_json,status,due_at,created_at,executed_at FROM actions`
	var where []string
	var args []any
	if clientID != "" {
		where = append(where, "client_id=?")
		args = append(args, clientID)
	}
	if convID != "" {
		where = append(where, "conversation_id=?")
		args = append(args, convID)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := d.Conn.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Action
	for rows.Next() {
		var a Action
		var pj string
		var due, exec sql.NullFloat64
		if err := rows.Scan(&a.ID, &a.ClientID, &a.ConvID, &a.Type, &pj, &a.Status, &due, &a.CreatedAt, &exec); err != nil {
			return nil, err
		}
		a.Payload = json.RawMessage(pj)
		if due.Valid {
			a.DueAt = &due.Float64
		}
		if exec.Valid {
			a.ExecutedAt = &exec.Float64
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

// ---------- Client docs ----------

type Doc struct {
	ID        string  `json:"id"`
	ClientID  string  `json:"client_id,omitempty"`
	Title     string  `json:"title"`
	MIME      string  `json:"mime"`
	Body      string  `json:"body"`
	CreatedAt float64 `json:"created_at"`
}

func (d *DB) InsertDoc(doc *Doc) error {
	if doc.ID == "" {
		doc.ID = uuid.NewString()
	}
	if doc.MIME == "" {
		doc.MIME = "text/plain"
	}
	doc.CreatedAt = now()
	_, err := d.Conn.Exec(
		`INSERT INTO client_docs(id,client_id,title,mime,body,created_at) VALUES(?,?,?,?,?,?)`,
		doc.ID, nullStr(doc.ClientID), doc.Title, doc.MIME, doc.Body, doc.CreatedAt,
	)
	return err
}

func (d *DB) DeleteDoc(id string) error {
	_, err := d.Conn.Exec(`DELETE FROM client_docs WHERE id=?`, id)
	return err
}

func (d *DB) ListDocs(clientID string) ([]*Doc, error) {
	q := `SELECT id,COALESCE(client_id,''),title,mime,body,created_at FROM client_docs`
	var args []any
	if clientID != "" {
		q += ` WHERE client_id=? OR client_id IS NULL`
		args = append(args, clientID)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := d.Conn.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Doc
	for rows.Next() {
		var doc Doc
		if err := rows.Scan(&doc.ID, &doc.ClientID, &doc.Title, &doc.MIME, &doc.Body, &doc.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &doc)
	}
	return out, rows.Err()
}

// ---------- Embeddings ----------

type EmbedRow struct {
	ID        string
	Kind      string
	RefID     string
	ClientID  string
	Text      string
	Vec       []float32
	CreatedAt float64
}

func (d *DB) InsertEmbed(e *EmbedRow) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	e.CreatedAt = now()
	blob := float32SliceToBytes(e.Vec)
	_, err := d.Conn.Exec(
		`INSERT INTO embeddings(id,kind,ref_id,client_id,text,vec,dim,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		e.ID, e.Kind, e.RefID, nullStr(e.ClientID), e.Text, blob, len(e.Vec), e.CreatedAt,
	)
	return err
}

func (d *DB) DeleteEmbeds(kind, refID string) error {
	_, err := d.Conn.Exec(`DELETE FROM embeddings WHERE kind=? AND ref_id=?`, kind, refID)
	return err
}

func (d *DB) IterEmbeds(clientID string, fn func(*EmbedRow) error) error {
	q := `SELECT id,kind,ref_id,COALESCE(client_id,''),text,vec,dim,created_at FROM embeddings`
	var args []any
	if clientID != "" {
		q += ` WHERE client_id=? OR client_id IS NULL OR client_id=''`
		args = append(args, clientID)
	}
	rows, err := d.Conn.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var e EmbedRow
		var blob []byte
		var dim int
		if err := rows.Scan(&e.ID, &e.Kind, &e.RefID, &e.ClientID, &e.Text, &blob, &dim, &e.CreatedAt); err != nil {
			return err
		}
		e.Vec = bytesToFloat32Slice(blob, dim)
		if err := fn(&e); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ---------- helpers ----------

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func pickAvatarColor(seed string) string {
	palette := []string{"#FFD6A5", "#CAFFBF", "#9BF6FF", "#BDB2FF", "#FFC6FF", "#FDFFB6", "#FFADAD", "#A0C4FF"}
	h := 0
	for _, c := range seed {
		h = (h*31 + int(c)) & 0x7fffffff
	}
	return palette[h%len(palette)]
}

func float32SliceToBytes(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		u := math.Float32bits(f)
		b[i*4+0] = byte(u)
		b[i*4+1] = byte(u >> 8)
		b[i*4+2] = byte(u >> 16)
		b[i*4+3] = byte(u >> 24)
	}
	return b
}

func bytesToFloat32Slice(b []byte, dim int) []float32 {
	out := make([]float32, dim)
	for i := 0; i < dim && (i*4+3) < len(b); i++ {
		u := uint32(b[i*4]) | uint32(b[i*4+1])<<8 | uint32(b[i*4+2])<<16 | uint32(b[i*4+3])<<24
		out[i] = math.Float32frombits(u)
	}
	return out
}

var ErrNotFound = errors.New("not found")
