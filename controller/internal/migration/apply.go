package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

func Apply(ctx context.Context, plan Plan) error {
	if err := validatePlan(plan); err != nil {
		return err
	}
	if err := verifySource(ctx, plan); err != nil {
		return err
	}
	initial := journal{Plan: plan, Moved: make([]bool, len(plan.Moves))}
	if _, err := verifyProgress(&initial); err != nil {
		return err
	}
	state := filepath.Dir(plan.DestinationDB)
	if err := makeDurableDirectory(state); err != nil {
		return err
	}
	lock, err := acquireMigrationLock(filepath.Join(state, "migration.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	value, err := beginMigration(ctx, plan)
	if err != nil {
		return err
	}
	if err = advanceMigration(ctx, &value); err != nil {
		return fmt.Errorf("migration stopped; resume with --resume %s: %w", JournalPath(plan), err)
	}
	return nil
}

func Resume(ctx context.Context, filename string) (Plan, error) {
	physical, err := physicalPath(filename)
	if err != nil || physical != filename {
		return Plan{}, fmt.Errorf("resume requires the canonical absolute journal path")
	}
	if _, err = fileIdentity(filename, false); err != nil {
		return Plan{}, err
	}
	lock, err := acquireMigrationLock(filepath.Join(filepath.Dir(filename), "migration.lock"))
	if err != nil {
		return Plan{}, err
	}
	defer lock.Close()
	value, err := readJournal(filename)
	if err != nil {
		return Plan{}, err
	}
	if err = advanceMigration(ctx, &value); err != nil {
		return Plan{}, err
	}
	return value.Plan, nil
}

func makeDurableDirectory(directory string) error {
	physical, err := physicalPath(directory)
	if err != nil || physical != directory {
		return fmt.Errorf("migration directory changed: %s", directory)
	}
	if _, err = fileIdentity(directory, true); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = makeDurableDirectory(filepath.Dir(directory)); err != nil {
		return err
	}
	if err = os.Mkdir(directory, 0700); err != nil {
		return err
	}
	return errors.Join(syncDirectory(directory), syncDirectory(filepath.Dir(directory)))
}

func requireAbsent(filename string) error {
	_, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("migration destination already exists: %s", filename)
}

func beginMigration(ctx context.Context, plan Plan) (journal, error) {
	value := journal{Version: 1, Plan: plan, Moved: make([]bool, len(plan.Moves))}
	if err := validatePlan(plan); err != nil {
		return value, err
	}
	if err := requireAbsent(JournalPath(plan)); err != nil {
		return value, fmt.Errorf("migration record exists; use --resume %s: %w", JournalPath(plan), err)
	}
	if err := requireAbsent(plan.DestinationDB); err != nil {
		return value, err
	}
	if err := verifySource(ctx, plan); err != nil {
		return value, err
	}
	locations, err := verifyProgress(&value)
	if err != nil {
		return value, err
	}
	for index, move := range plan.Moves {
		if locations[index] != move.From {
			return value, fmt.Errorf("fresh migration requires all original source directories")
		}
		if err = makeDurableDirectory(filepath.Dir(move.To)); err != nil {
			return value, err
		}
		parent, err := fileIdentity(filepath.Dir(move.To), true)
		if err != nil {
			return value, err
		}
		if parent.Device != move.Identity.Device {
			return value, fmt.Errorf("migration requires an atomic rename on the same filesystem")
		}
	}
	value.TargetSHA256, err = targetFingerprint(plan)
	if err != nil {
		return value, err
	}
	value.Stage = plan.DestinationDB + ".migration-" + uuid.NewString()
	file, err := os.OpenFile(value.Stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return value, err
	}
	if err = errors.Join(file.Sync(), file.Close(), syncDirectory(filepath.Dir(value.Stage))); err != nil {
		return value, err
	}
	value.StageIdentity, err = fileIdentity(value.Stage, false)
	if err != nil {
		return value, err
	}
	err = writeJournal(JournalPath(plan), &value)
	return value, err
}

func advanceMigration(ctx context.Context, value *journal) error {
	if err := verifySource(ctx, value.Plan); err != nil {
		return err
	}
	locations, err := verifyProgress(value)
	if err != nil {
		return err
	}
	stage, stageErr := fileIdentity(value.Stage, false)
	target, targetErr := fileIdentity(value.Plan.DestinationDB, false)
	if stageErr == nil && stage == value.StageIdentity && errors.Is(targetErr, os.ErrNotExist) && !value.Complete {
		if !value.DatabaseReady {
			if err = prepareDatabase(ctx, value); err != nil {
				return err
			}
			value.DatabaseReady = true
			if err = writeJournal(JournalPath(value.Plan), value); err != nil {
				return err
			}
		}
		actual, err := fingerprintFile(ctx, value.Stage)
		if err != nil {
			return err
		}
		if actual != value.TargetSHA256 {
			return fmt.Errorf("staged database fingerprint differs")
		}
	} else if errors.Is(stageErr, os.ErrNotExist) && targetErr == nil && target == value.StageIdentity && value.DatabaseReady {
		for index, move := range value.Plan.Moves {
			if locations[index] != move.To || !value.Moved[index] {
				return fmt.Errorf("published database has incomplete directory moves")
			}
		}
		actual, err := fingerprintFile(ctx, value.Plan.DestinationDB)
		if err != nil {
			return err
		}
		if actual != value.TargetSHA256 {
			return fmt.Errorf("published database fingerprint differs")
		}
		value.Complete = true
		return writeJournal(JournalPath(value.Plan), value)
	} else {
		return fmt.Errorf("migration database has mixed or changed source/target")
	}
	for index := range value.Plan.Moves {
		if err = moveDirectory(value, index); err != nil {
			return err
		}
	}
	if err = verifySource(ctx, value.Plan); err != nil {
		return err
	}
	if _, err = verifyProgress(value); err != nil {
		return err
	}
	if err = requireAbsent(value.Plan.DestinationDB); err != nil {
		return err
	}
	if err = renameExclusive(value.Stage, value.Plan.DestinationDB); err != nil {
		return err
	}
	if err = syncDirectory(filepath.Dir(value.Stage)); err != nil {
		return err
	}
	value.Complete = true
	return writeJournal(JournalPath(value.Plan), value)
}

func moveDirectory(value *journal, index int) error {
	locations, err := verifyProgress(value)
	if err != nil {
		return err
	}
	move := value.Plan.Moves[index]
	if locations[index] == move.From {
		if err = renameExclusive(move.From, move.To); err != nil {
			return err
		}
	}
	if err = errors.Join(syncDirectory(filepath.Dir(move.From)), syncDirectory(filepath.Dir(move.To))); err != nil {
		return err
	}
	value.Moved[index] = true
	return writeJournal(JournalPath(value.Plan), value)
}

func prepareDatabase(ctx context.Context, value *journal) error {
	u := url.URL{Scheme: "file", Path: value.Stage, RawQuery: "mode=rw&_pragma=journal_mode(DELETE)&_pragma=synchronous(FULL)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS records(kind TEXT NOT NULL,id TEXT NOT NULL,data BLOB NOT NULL,PRIMARY KEY(kind,id)); CREATE TABLE IF NOT EXISTS counters(name TEXT PRIMARY KEY,value INTEGER NOT NULL)"); err != nil {
		return err
	}
	for _, record := range value.Plan.Records {
		data, err := json.Marshal(record.Data)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO records(kind,id,data) VALUES(?,?,?) ON CONFLICT(kind,id) DO NOTHING", record.Kind, value.Plan.Project+"/"+record.ID, data); err != nil {
			return err
		}
	}
	for name, counter := range value.Plan.Counters {
		if _, err = tx.ExecContext(ctx, "INSERT INTO counters(name,value) VALUES(?,?) ON CONFLICT(name) DO NOTHING", value.Plan.Project+"/"+name, counter); err != nil {
			return err
		}
	}
	actual, err := databaseFingerprint(ctx, tx)
	if err != nil {
		return err
	}
	if actual != value.TargetSHA256 {
		return fmt.Errorf("staged database contains records outside the migration plan")
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if err = db.Close(); err != nil {
		return err
	}
	return errors.Join(syncFile(value.Stage), syncDirectory(filepath.Dir(value.Stage)))
}
