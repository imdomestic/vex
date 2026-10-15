package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestTransactionAndCounterPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("abort transaction")
	err = db.Update(ctx, func(tx *Tx) error {
		if err := tx.Put(ctx, "room", "one", map[string]int{"generation": 1}); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	var room map[string]int
	if err = db.Get(ctx, "room", "one", &room); !errors.Is(err, ErrNotFound) {
		t.Fatal("aborted transaction persisted")
	}
	for i := 1; i <= 2; i++ {
		err = db.Update(ctx, func(tx *Tx) error {
			n, err := tx.Next(ctx, "attempt")
			if err != nil {
				return err
			}
			if n != i {
				t.Fatalf("counter=%d want=%d", n, i)
			}
			return tx.Put(ctx, "build", "one", n)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Delete(ctx, "build", "one"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.Update(ctx, func(tx *Tx) error {
		n, err := tx.Next(ctx, "attempt")
		if err != nil {
			return err
		}
		if n != 3 {
			t.Fatalf("counter after eviction/reopen=%d", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
