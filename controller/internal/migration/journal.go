package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/filehash"
	"golang.org/x/sys/unix"
)

type FileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type journal struct {
	Version        int          `json:"version"`
	Plan           Plan         `json:"plan"`
	Stage          string       `json:"stageDatabase"`
	StageIdentity  FileIdentity `json:"stageIdentity"`
	TargetSHA256   string       `json:"targetStateSHA256"`
	Moved          []bool       `json:"moved"`
	DatabaseReady  bool         `json:"databaseReady"`
	Complete       bool         `json:"complete"`
	ChecksumSHA256 string       `json:"journalSHA256"`
}

func physicalPath(filename string) (string, error) {
	if !filepath.IsAbs(filename) {
		return "", fmt.Errorf("migration paths must be absolute")
	}
	current := filepath.Clean(filename)
	var missing []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		current = parent
	}
}

func pathWithin(parent, filename string) bool {
	relative, err := filepath.Rel(parent, filename)
	return err == nil && filepath.IsLocal(relative)
}

func pathsOverlap(first, second string) bool {
	return pathWithin(first, second) || pathWithin(second, first)
}

func fileIdentity(filename string, directory bool) (FileIdentity, error) {
	var info unix.Stat_t
	if err := unix.Lstat(filename, &info); err != nil {
		return FileIdentity{}, err
	}
	wanted := uint32(unix.S_IFREG)
	if directory {
		wanted = unix.S_IFDIR
	}
	if uint32(info.Mode)&unix.S_IFMT != wanted {
		return FileIdentity{}, fmt.Errorf("unexpected migration file type: %s", filename)
	}
	return FileIdentity{Device: uint64(info.Dev), Inode: uint64(info.Ino)}, nil
}

func jsonDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func planDigest(plan Plan) (string, error) {
	plan.SHA256 = ""
	return jsonDigest(plan)
}

func validatePlan(plan Plan) error {
	digest, err := planDigest(plan)
	if err != nil {
		return err
	}
	if !config.ValidID(plan.Project) || digest != plan.SHA256 || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(plan.SourceState) {
		return fmt.Errorf("migration plan checksum or identity differs")
	}
	target := filepath.Dir(filepath.Dir(plan.DestinationDB))
	if plan.DestinationDB != filepath.Join(target, "state", "vexd.db") || pathsOverlap(plan.SourceDir, target) || pathWithin(target, plan.SourceDB) {
		return fmt.Errorf("migration source and destination overlap")
	}
	for _, filename := range []string{plan.SourceDir, plan.SourceDB, target, plan.DestinationDB} {
		physical, err := physicalPath(filename)
		if err != nil || physical != filename {
			return fmt.Errorf("migration path identity changed: %s", filename)
		}
	}
	allowed := map[string]string{"snapshots": "snapshots", "assets": "worlds", "packs": "packs", "gradle-home": "cache/gradle"}
	seen := map[string]bool{}
	for _, move := range plan.Moves {
		name := filepath.Base(move.From)
		mapped, ok := allowed[name]
		if !ok || seen[name] || move.From != filepath.Join(plan.SourceDir, name) || move.To != filepath.Join(target, "projects", plan.Project, mapped) || move.Identity.Inode == 0 {
			return fmt.Errorf("migration move does not match the project layout")
		}
		seen[name] = true
		if pathWithin(move.From, plan.SourceDB) {
			return fmt.Errorf("source database cannot be inside a moved directory")
		}
	}
	for filename, checksum := range plan.SourceHashes {
		found := false
		for _, move := range plan.Moves {
			found = found || (filename != move.From && pathWithin(move.From, filename))
		}
		if !found || filepath.Clean(filename) != filename || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(checksum) {
			return fmt.Errorf("migration file hash is outside planned directories")
		}
	}
	return nil
}

func JournalPath(plan Plan) string {
	return filepath.Join(filepath.Dir(plan.DestinationDB), "migration-"+plan.Project+".json")
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func syncFile(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func writeJournal(filename string, value *journal) error {
	value.ChecksumSHA256 = ""
	checksum, err := jsonDigest(value)
	if err != nil {
		return err
	}
	value.ChecksumSHA256 = checksum
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	staging := filename + ".write-" + uuid.NewString()
	file, err := os.OpenFile(staging, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(data, '\n'))
	if err = errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	if err = os.Rename(staging, filename); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(filename))
}

func readJournal(filename string) (journal, error) {
	var value journal
	if _, err := fileIdentity(filename, false); err != nil {
		return value, err
	}
	file, err := os.Open(filename)
	if err != nil {
		return value, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64*1024*1024))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("invalid migration journal")
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return value, fmt.Errorf("migration journal contains trailing content")
	}
	stored := value.ChecksumSHA256
	value.ChecksumSHA256 = ""
	digest, err := jsonDigest(value)
	value.ChecksumSHA256 = stored
	if err != nil || digest != stored || value.Version != 1 || len(value.Moved) != len(value.Plan.Moves) {
		return value, fmt.Errorf("migration journal checksum or version differs")
	}
	if err = validatePlan(value.Plan); err != nil {
		return value, err
	}
	if JournalPath(value.Plan) != filename || filepath.Dir(value.Stage) != filepath.Dir(filename) || !strings.HasPrefix(filepath.Base(value.Stage), "vexd.db.migration-") || value.StageIdentity.Inode == 0 {
		return value, fmt.Errorf("migration journal paths differ")
	}
	expected, err := targetFingerprint(value.Plan)
	if err != nil || expected != value.TargetSHA256 {
		return value, fmt.Errorf("migration destination fingerprint differs")
	}
	return value, nil
}

func acquireMigrationLock(filename string) (*os.File, error) {
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("another migration is active: %w", err)
	}
	return file, nil
}

func fingerprintFile(ctx context.Context, filename string) (string, error) {
	if _, err := fileIdentity(filename, false); err != nil {
		return "", err
	}
	u := url.URL{Scheme: "file", Path: filename, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return "", err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	return databaseFingerprint(ctx, tx)
}

func verifySource(ctx context.Context, plan Plan) error {
	actual, err := fingerprintFile(ctx, plan.SourceDB)
	if err != nil {
		return err
	}
	if actual != plan.SourceState {
		return fmt.Errorf("source database changed after planning")
	}
	return nil
}

func verifyProgress(value *journal) ([]string, error) {
	locations := make([]string, len(value.Plan.Moves))
	for index, move := range value.Plan.Moves {
		for _, filename := range []string{move.From, move.To} {
			physical, err := physicalPath(filename)
			if err != nil || physical != filename {
				return nil, fmt.Errorf("migration directory path changed: %s", filename)
			}
		}
		source, sourceErr := fileIdentity(move.From, true)
		target, targetErr := fileIdentity(move.To, true)
		if sourceErr == nil && errors.Is(targetErr, os.ErrNotExist) && !value.Moved[index] && source == move.Identity {
			locations[index] = move.From
		} else if errors.Is(sourceErr, os.ErrNotExist) && targetErr == nil && target == move.Identity {
			locations[index] = move.To
		} else {
			return nil, fmt.Errorf("migration directory has mixed or changed source/target: %s", move.From)
		}
	}
	actualFiles := map[string]string{}
	for index, move := range value.Plan.Moves {
		if filepath.Base(move.From) == "gradle-home" {
			continue
		}
		if err := walkHashes(locations[index], func(filename, checksum string) error {
			relative, err := filepath.Rel(locations[index], filename)
			if err != nil {
				return err
			}
			actualFiles[filepath.Join(move.From, relative)] = checksum
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if len(actualFiles) != len(value.Plan.SourceHashes) {
		return nil, fmt.Errorf("migration file inventory changed")
	}
	for filename, expected := range value.Plan.SourceHashes {
		if actualFiles[filename] != expected {
			return nil, fmt.Errorf("migration file hash changed: %s", filename)
		}
	}
	return locations, nil
}

func walkHashes(directory string, visit func(string, string) error) error {
	return filepath.WalkDir(directory, func(filename string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if _, err = fileIdentity(filename, false); err != nil {
			return err
		}
		hashes, err := filehash.File(filename)
		if err != nil {
			return err
		}
		return visit(filename, hashes.SHA256)
	})
}

func collectSourceHashes(plan *Plan) error {
	for _, move := range plan.Moves {
		if filepath.Base(move.From) == "gradle-home" {
			continue
		}
		if err := walkHashes(move.From, func(filename, checksum string) error {
			if expected, exists := plan.SourceHashes[filename]; exists && expected != checksum {
				return fmt.Errorf("source file changed during planning: %s", filename)
			}
			plan.SourceHashes[filename] = checksum
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func targetFingerprint(plan Plan) (string, error) {
	records := slices.Clone(plan.Records)
	slices.SortFunc(records, func(first, second Record) int {
		return strings.Compare(first.Kind+"\x00"+first.ID, second.Kind+"\x00"+second.ID)
	})
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	for _, record := range records {
		data, err := json.Marshal(record.Data)
		if err != nil {
			return "", err
		}
		if err = encoder.Encode([3]string{record.Kind, plan.Project + "/" + record.ID, string(data)}); err != nil {
			return "", err
		}
	}
	names := make([]string, 0, len(plan.Counters))
	for name := range plan.Counters {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := encoder.Encode([3]string{"counter", plan.Project + "/" + name, strconv.FormatInt(plan.Counters[name], 10)}); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
