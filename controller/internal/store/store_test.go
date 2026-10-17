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
	root := db
	db, err = root.WithProject("alpha")
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
	if err = root.Close(); err != nil {
		t.Fatal(err)
	}
	root, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	db, err = root.WithProject("alpha")
	if err != nil {
		t.Fatal(err)
	}
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

func TestProjectIsolationAndAtomicView(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.WithProject("alpha")
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.WithProject("beta")
	if err != nil {
		t.Fatal(err)
	}
	for index, scoped := range []*Store{a, b} {
		if err = scoped.Update(ctx, func(tx *Tx) error {
			counter, err := tx.Next(ctx, "generation")
			if err != nil || counter != 1 {
				t.Fatalf("project-local counter: %d, %v", counter, err)
			}
			return tx.Put(ctx, "room", "main-fabric", index+1)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.List(ctx, "room"); !errors.Is(err, ErrProjectRequired) {
		t.Fatal("unscoped list accepted")
	}
	if err = db.Put(ctx, "room", "main-fabric", 3); !errors.Is(err, ErrProjectRequired) {
		t.Fatal("unscoped write accepted")
	}
	if _, err = db.WithProject("alpha--beta"); err == nil {
		t.Fatal("ambiguous project ID accepted")
	}
	err = db.View(ctx, func(view *View) error {
		for index, project := range []string{"alpha", "beta"} {
			scoped, err := view.WithProject(project)
			if err != nil {
				return err
			}
			values, err := All[int](ctx, scoped, "room")
			if err != nil || len(values) != 1 || values[0] != index+1 {
				t.Fatalf("project isolation: %v, %v", values, err)
			}
			if err = scoped.Delete(ctx, "room", "main-fabric"); !errors.Is(err, ErrReadOnly) {
				t.Fatal("snapshot allowed a mutation")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Delete(ctx, "room", "main-fabric"); err != nil {
		t.Fatal(err)
	}
	var value int
	if err = b.Get(ctx, "room", "main-fabric", &value); err != nil || value != 2 {
		t.Fatal("cross-project delete changed another project")
	}
	rows, err := db.db.QueryContext(ctx, "SELECT id FROM records ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("missing physical record")
	}
	var key string
	if err = rows.Scan(&key); err != nil || key != "beta/main-fabric" {
		t.Fatal("record key is not project-prefixed")
	}
}
