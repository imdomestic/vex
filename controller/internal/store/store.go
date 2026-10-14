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

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("record not found")

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

type records struct { executor }
type Store struct { records; db *sql.DB }
type Tx struct { records; tx *sql.Tx }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path),0700); err != nil { return nil,err }
	u := url.URL{Scheme:"file",Path:path}
	q := u.Query()
	q.Add("_pragma","busy_timeout(10000)")
	q.Add("_pragma","journal_mode(WAL)")
	q.Add("_pragma","foreign_keys(1)")
	q.Add("_txlock","immediate")
	u.RawQuery = q.Encode()
	db,err := sql.Open("sqlite",u.String())
	if err != nil { return nil,err }
	db.SetMaxOpenConns(1)
	_,err = db.Exec(`CREATE TABLE IF NOT EXISTS records (kind TEXT NOT NULL,id TEXT NOT NULL,data BLOB NOT NULL,PRIMARY KEY(kind,id)); CREATE TABLE IF NOT EXISTS counters (name TEXT PRIMARY KEY,value INTEGER NOT NULL);`)
	if err != nil { db.Close(); return nil,err }
	return &Store{records:records{db},db:db},nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Update(ctx context.Context, fn func(*Tx) error) error {
	tx,err := s.db.BeginTx(ctx,nil)
	if err != nil { return err }
	defer tx.Rollback()
	if err = fn(&Tx{records:records{tx},tx:tx}); err != nil { return err }
	return tx.Commit()
}

func (r records) Get(ctx context.Context, kind,id string,out any) error {
	var data []byte
	err := r.QueryRowContext(ctx,"SELECT data FROM records WHERE kind=? AND id=?",kind,id).Scan(&data)
	if errors.Is(err,sql.ErrNoRows) { return ErrNotFound }
	if err != nil { return err }
	return json.Unmarshal(data,out)
}

func (r records) List(ctx context.Context, kind string) ([]json.RawMessage,error) {
	rows,err := r.QueryContext(ctx,"SELECT data FROM records WHERE kind=? ORDER BY id",kind)
	if err != nil { return nil,err }
	defer rows.Close()
	result := []json.RawMessage{}
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil { return nil,err }
		result = append(result,json.RawMessage(data))
	}
	return result,rows.Err()
}

func (r records) Put(ctx context.Context, kind,id string,value any) error {
	if kind == "" || id == "" { return fmt.Errorf("record kind and id are required") }
	data,err := json.Marshal(value)
	if err != nil { return err }
	_,err = r.ExecContext(ctx,"INSERT INTO records(kind,id,data) VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET data=excluded.data",kind,id,data)
	return err
}

func (r records) Delete(ctx context.Context,kind,id string) error {
	_,err := r.ExecContext(ctx,"DELETE FROM records WHERE kind=? AND id=?",kind,id)
	return err
}

func (t *Tx) Next(ctx context.Context,name string) (int,error) {
	var value int
	err := t.QueryRowContext(ctx,"INSERT INTO counters(name,value) VALUES(?,1) ON CONFLICT(name) DO UPDATE SET value=value+1 RETURNING value",name).Scan(&value)
	return value,err
}

func All[T any](ctx context.Context,s Access,kind string) ([]T,error) {
	data,err := s.List(ctx,kind)
	if err != nil { return nil,err }
	result := make([]T,0,len(data))
	for _,raw := range data {
		var value T
		if err = json.Unmarshal(raw,&value); err != nil { return nil,err }
		result = append(result,value)
	}
	return result,nil
}
