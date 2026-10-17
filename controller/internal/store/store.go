package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/imdomestic/vex/controller/internal/config"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("record not found")
var ErrProjectRequired = errors.New("project scope is required")
var ErrReadOnly = errors.New("read-only snapshot")

type Access interface {
	Get(context.Context, string, string, any) error
	List(context.Context, string) ([]json.RawMessage, error)
	Put(context.Context, string, string, any) error
	Delete(context.Context, string, string) error
}

type executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type records struct {
	executor
	project  string
	readOnly bool
}
type Store struct {
	records
	db    *sql.DB
	owner bool
}
type Tx struct {
	records
	tx *sql.Tx
}
type View struct {
	records
	tx *sql.Tx
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS records (kind TEXT NOT NULL,id TEXT NOT NULL,data BLOB NOT NULL,PRIMARY KEY(kind,id)); CREATE TABLE IF NOT EXISTS counters (name TEXT PRIMARY KEY,value INTEGER NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{records: records{executor: db}, db: db, owner: true}, nil
}

func (s *Store) Close() error {
	if !s.owner {
		return nil
	}
	return s.db.Close()
}

func (s *Store) WithProject(project string) (*Store, error) {
	if !config.ValidID(project) {
		return nil, fmt.Errorf("invalid project ID")
	}
	return &Store{records: records{executor: s.db, project: project}, db: s.db}, nil
}

func (s *Store) Project() string { return s.project }

func (s *Store) Platform() (*Store, error) {
	if s.project != "" {
		return nil, fmt.Errorf("platform scope requires the root store")
	}
	return &Store{records: records{executor: s.db, project: "@platform"}, db: s.db}, nil
}

func (s *Store) View(ctx context.Context, fn func(*View) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(&View{records: records{executor: tx, project: s.project, readOnly: true}, tx: tx}); err != nil {
		return err
	}
	return tx.Commit()
}

func (v *View) WithProject(project string) (Access, error) {
	if !config.ValidID(project) {
		return nil, fmt.Errorf("invalid project ID")
	}
	return records{executor: v.tx, project: project, readOnly: true}, nil
}

func (s *Store) Update(ctx context.Context, fn func(*Tx) error) error {
	if s.project == "" {
		return ErrProjectRequired
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(&Tx{records: records{executor: tx, project: s.project}, tx: tx}); err != nil {
		return err
	}
	return tx.Commit()
}

func (r records) Get(ctx context.Context, kind, id string, out any) error {
	key, err := r.key(kind, id)
	if err != nil {
		return err
	}
	var data []byte
	err = r.QueryRowContext(ctx, "SELECT data FROM records WHERE kind=? AND id=?", kind, key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (r records) List(ctx context.Context, kind string) ([]json.RawMessage, error) {
	if r.project == "" {
		return nil, ErrProjectRequired
	}
	if kind == "" {
		return nil, fmt.Errorf("record kind is required")
	}
	prefix := r.project + "/"
	rows, err := r.QueryContext(ctx, "SELECT data FROM records WHERE kind=? AND substr(id,1,?)=? ORDER BY id", kind, len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []json.RawMessage{}
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		result = append(result, json.RawMessage(data))
	}
	return result, rows.Err()
}

func (r records) Put(ctx context.Context, kind, id string, value any) error {
	if r.readOnly {
		return ErrReadOnly
	}
	key, err := r.key(kind, id)
	if err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = r.ExecContext(ctx, "INSERT INTO records(kind,id,data) VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET data=excluded.data", kind, key, data)
	return err
}

func (r records) Delete(ctx context.Context, kind, id string) error {
	if r.readOnly {
		return ErrReadOnly
	}
	key, err := r.key(kind, id)
	if err != nil {
		return err
	}
	_, err = r.ExecContext(ctx, "DELETE FROM records WHERE kind=? AND id=?", kind, key)
	return err
}

func (t *Tx) Next(ctx context.Context, name string) (int, error) {
	key, err := t.key("counter", name)
	if err != nil {
		return 0, err
	}
	var value int
	err = t.QueryRowContext(ctx, "INSERT INTO counters(name,value) VALUES(?,1) ON CONFLICT(name) DO UPDATE SET value=value+1 RETURNING value", key).Scan(&value)
	return value, err
}

func (r records) key(kind, id string) (string, error) {
	if r.project == "" {
		return "", ErrProjectRequired
	}
	if kind == "" || id == "" || strings.ContainsAny(kind, "/\x00") || strings.ContainsAny(id, "/\x00") {
		return "", fmt.Errorf("record kind and local ID must be nonempty single components")
	}
	return r.project + "/" + id, nil
}

func All[T any](ctx context.Context, s Access, kind string) ([]T, error) {
	data, err := s.List(ctx, kind)
	if err != nil {
		return nil, err
	}
	result := make([]T, 0, len(data))
	for _, raw := range data {
		var value T
		if err = json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}
