package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/filehash"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/imdomestic/vex/controller/internal/worldarchive"
	"github.com/pelletier/go-toml/v2"
)

type Options struct {
	FromDB    string
	FromDir   string
	ToDir     string
	Project   string
	Zone      string
	UsersFile string
}

type Move struct {
	From     string       `json:"from"`
	To       string       `json:"to"`
	Identity FileIdentity `json:"identity"`
}

type Record struct {
	Kind string          `json:"kind"`
	ID   string          `json:"id"`
	Data json.RawMessage `json:"data"`
}

type Plan struct {
	Project       string            `json:"project"`
	SourceDir     string            `json:"sourceDir"`
	SourceDB      string            `json:"sourceDB"`
	DestinationDB string            `json:"destinationDB"`
	Moves         []Move            `json:"moves"`
	Records       []Record          `json:"records"`
	Counters      map[string]int64  `json:"counters"`
	Discarded     map[string]int    `json:"discarded"`
	TokenSettings string            `json:"tokenSettings"`
	MainSeed      model.Snapshot    `json:"mainSeed"`
	SourceHashes  map[string]string `json:"sourceHashes"`
	SourceState   string            `json:"sourceStateSHA256"`
	SHA256        string            `json:"planSHA256"`
}

func Prepare(ctx context.Context, options Options) (Plan, error) {
	plan := Plan{Project: options.Project, Moves: []Move{}, Records: []Record{}, Counters: map[string]int64{}, Discarded: map[string]int{}, SourceHashes: map[string]string{}}
	if !config.ValidID(options.Project) || options.Zone == "" {
		return plan, fmt.Errorf("migration requires a valid project ID and zone")
	}
	var err error
	for _, filename := range []*string{&options.FromDB, &options.FromDir, &options.ToDir} {
		*filename, err = physicalPath(*filename)
		if err != nil {
			return plan, err
		}
	}
	if pathsOverlap(options.FromDir, options.ToDir) || pathWithin(options.ToDir, options.FromDB) {
		return plan, fmt.Errorf("source and destination paths must not overlap")
	}
	plan.SourceDir, plan.SourceDB = options.FromDir, options.FromDB
	plan.DestinationDB = filepath.Join(options.ToDir, "state", "vexd.db")
	if _, err := os.Stat(plan.DestinationDB); err == nil {
		return plan, fmt.Errorf("destination database already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return plan, err
	}
	projectDir := filepath.Join(options.ToDir, "projects", options.Project)
	for _, pair := range [][2]string{{"snapshots", "snapshots"}, {"assets", "worlds"}, {"packs", "packs"}, {"gradle-home", "cache/gradle"}} {
		from, to := filepath.Join(options.FromDir, pair[0]), filepath.Join(projectDir, pair[1])
		info, err := os.Lstat(from)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return plan, err
		}
		if !info.IsDir() {
			return plan, fmt.Errorf("migration source is not a directory: %s", from)
		}
		if _, err = os.Lstat(to); err == nil {
			return plan, fmt.Errorf("migration destination already exists: %s", to)
		} else if !errors.Is(err, os.ErrNotExist) {
			return plan, err
		}
		identity, err := fileIdentity(from, true)
		if err != nil {
			return plan, err
		}
		plan.Moves = append(plan.Moves, Move{From: from, To: to, Identity: identity})
		if pathWithin(from, options.FromDB) {
			return plan, fmt.Errorf("source database cannot be inside a moved directory")
		}
	}
	u := url.URL{Scheme: "file", Path: options.FromDB}
	u.RawQuery = "mode=ro"
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return plan, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return plan, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT kind,id,data FROM records ORDER BY kind,id")
	if err != nil {
		return plan, err
	}
	var main *model.Room
	for rows.Next() {
		var record Record
		if err = rows.Scan(&record.Kind, &record.ID, &record.Data); err != nil {
			rows.Close()
			return plan, err
		}
		switch record.Kind {
		case "build", "build_access", "branch", "room_runtime", "room_failure_log", "lease":
			plan.Discarded[record.Kind]++
			continue
		case "room", "room_tombstone", "snapshot", "asset", "asset_revision", "pack_revision", "transfer":
		default:
			rows.Close()
			return plan, fmt.Errorf("unsupported source record kind %q", record.Kind)
		}
		converted, err := convert(options, &plan, record)
		if err != nil {
			rows.Close()
			return plan, fmt.Errorf("migrate %s/%s: %w", record.Kind, record.ID, err)
		}
		if converted.Kind == "snapshot" {
			var snapshot model.Snapshot
			if err = json.Unmarshal(converted.Data, &snapshot); err != nil {
				rows.Close()
				return plan, err
			}
			if snapshot.RoomID == "main-fabric" && snapshot.RoomGeneration > plan.MainSeed.RoomGeneration {
				plan.MainSeed = snapshot
			}
		}
		if converted.Kind == "room" {
			var room model.Room
			if err = json.Unmarshal(converted.Data, &room); err != nil {
				rows.Close()
				return plan, err
			}
			if room.ID == "main-fabric" {
				main = &room
				continue
			}
			if room.State != "closed" {
				rows.Close()
				return plan, fmt.Errorf("room %s must be closed before migration", room.ID)
			}
		}
		plan.Records = append(plan.Records, converted)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return plan, err
	}
	if main == nil || plan.MainSeed.ID == "" || !plan.MainSeed.LocalAvailable || plan.MainSeed.TankVerifiedAt == nil {
		return plan, fmt.Errorf("main room requires a locally available, archive-verified snapshot")
	}
	if main.State != "stopped" && main.State != "closed" {
		return plan, fmt.Errorf("main room must be stopped before migration")
	}
	main.State, main.Phase = "stopped", "stopped"
	main.Seed = "snapshot:" + plan.MainSeed.ID
	main.SeedResolved = &model.SeedResolved{Kind: "snapshot", ID: plan.MainSeed.ID}
	main.Generation = max(main.Generation, plan.MainSeed.RoomGeneration)
	main.Players, main.Node, main.LastError, main.ClosedAt = []model.Player{}, nil, nil, nil
	main.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(main)
	if err != nil {
		return plan, err
	}
	plan.Records = append(plan.Records, Record{Kind: "room", ID: main.ID, Data: data})
	rows, err = tx.QueryContext(ctx, "SELECT name,value FROM counters ORDER BY name")
	if err != nil {
		return plan, err
	}
	for rows.Next() {
		var name string
		var value int64
		if err = rows.Scan(&name, &value); err != nil {
			rows.Close()
			return plan, err
		}
		name = strings.Replace(name, "asset_revision:", "world_revision:", 1)
		name = strings.Replace(name, "asset_restore:", "world_restore:", 1)
		plan.Counters[name] = value
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return plan, err
	}
	plan.SourceState, err = databaseFingerprint(ctx, tx)
	if err != nil {
		return plan, err
	}
	if err = tx.Commit(); err != nil {
		return plan, err
	}
	if options.UsersFile != "" {
		plan.TokenSettings, err = TokenSettings(options.UsersFile)
	}
	if err != nil {
		return plan, err
	}
	if err = collectSourceHashes(&plan); err != nil {
		return plan, err
	}
	plan.SHA256, err = planDigest(plan)
	return plan, err
}

func convert(options Options, plan *Plan, record Record) (Record, error) {
	var data map[string]json.RawMessage
	if err := json.Unmarshal(record.Data, &data); err != nil {
		return record, err
	}
	project, err := json.Marshal(options.Project)
	if err != nil {
		return record, err
	}
	data["project"] = project
	if asset, ok := data["asset"]; ok {
		data["world"] = asset
		delete(data, "asset")
	}
	if kind, ok := data["kind"]; ok && string(kind) == `"asset"` {
		data["kind"] = json.RawMessage(`"world"`)
	}
	for _, key := range []string{"requires", "seedResolved"} {
		if raw := data[key]; len(raw) > 0 && string(raw) != "null" {
			var object map[string]json.RawMessage
			if err = json.Unmarshal(raw, &object); err != nil {
				return record, err
			}
			if key == "requires" {
				var required bool
				if raw, ok := object["chorusMod"]; ok {
					if err = json.Unmarshal(raw, &required); err != nil {
						return record, err
					}
				}
				mods := []string{}
				if required {
					mods = append(mods, "chorus")
				}
				object["mods"], err = json.Marshal(mods)
				delete(object, "chorusMod")
			} else if string(object["kind"]) == `"asset"` {
				object["kind"] = json.RawMessage(`"world"`)
			}
			if err != nil {
				return record, err
			}
			data[key], err = json.Marshal(object)
			if err != nil {
				return record, err
			}
		}
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return record, err
	}
	var value any
	switch record.Kind {
	case "room":
		var room model.Room
		if err = json.Unmarshal(encoded, &room); err != nil {
			return record, err
		}
		room.Host = room.ID + "--" + options.Project + "." + options.Zone
		room.Seed = strings.Replace(room.Seed, "asset:", "world:", 1)
		room.Players, room.Node = []model.Player{}, nil
		value = room
	case "snapshot":
		var snapshot model.Snapshot
		if err = json.Unmarshal(encoded, &snapshot); err != nil {
			return record, err
		}
		snapshot.Path = path.Join("snapshots", snapshot.RoomID, snapshot.ID+".tar.gz")
		hashes, err := migrationHashes(options, plan, snapshot.Path, snapshot.SHA256, snapshot.Size)
		if err != nil {
			return record, err
		}
		snapshot.SHA1, snapshot.SHA512 = hashes.SHA1, hashes.SHA512
		if err = worldarchive.Validate(filepath.Join(options.FromDir, snapshot.Path), snapshot.Minecraft, "chorus-checkpoint.json"); err != nil {
			return record, err
		}
		value = snapshot
	case "asset":
		record.Kind = "world"
		value = data
	case "asset_revision":
		record.Kind = "world_revision"
		var revision model.WorldRevision
		if err = json.Unmarshal(encoded, &revision); err != nil {
			return record, err
		}
		hashes, err := migrationHashes(options, plan, path.Join("assets", revision.ID, revision.Rev, "world.tar.gz"), revision.World.SHA256, revision.World.Size)
		if err != nil {
			return record, err
		}
		revision.World.Path = path.Join("worlds", revision.ID, revision.Rev, "world.tar.gz")
		revision.World.SHA1, revision.World.SHA512 = hashes.SHA1, hashes.SHA512
		if err = worldarchive.Validate(filepath.Join(options.FromDir, "assets", revision.ID, revision.Rev, "world.tar.gz"), revision.Minecraft, "chorus-checkpoint.json"); err != nil {
			return record, err
		}
		revision.World.URL = "/v2/projects/" + options.Project + "/worlds/" + revision.ID + "/revisions/" + revision.Rev + "/download"
		value = revision
	case "pack_revision":
		var pack model.PackRevision
		if err = json.Unmarshal(encoded, &pack); err != nil {
			return record, err
		}
		pack.Path = path.Join("packs", pack.ID, pack.Rev+".zip")
		hashes, err := migrationHashes(options, plan, pack.Path, pack.SHA256, pack.Size)
		if err != nil {
			return record, err
		}
		pack.SHA1, pack.SHA512 = hashes.SHA1, hashes.SHA512
		pack.URL = "/v2/projects/" + options.Project + "/packs/" + pack.ID + "/" + pack.Rev + ".zip"
		value = pack
	case "transfer":
		var transfer model.Transfer
		if err = json.Unmarshal(encoded, &transfer); err != nil {
			return record, err
		}
		if transfer.Status != "verified" || transfer.VerifiedAt == nil {
			return record, fmt.Errorf("transfer must be verified before migration")
		}
		if !filepath.IsLocal(transfer.LocalPath) || transfer.RemotePath != transfer.LocalPath {
			return record, fmt.Errorf("unexpected legacy transfer path")
		}
		if _, err = migrationHashes(options, plan, transfer.LocalPath, transfer.SHA256, transfer.Size); err != nil {
			return record, err
		}
		transfer.LocalPath = strings.Replace(transfer.LocalPath, "assets/", "worlds/", 1)
		transfer.RemotePath = path.Join("projects", options.Project, transfer.LocalPath)
		transfer.ID = model.TransferID(transfer.Kind, transfer.ObjectID, transfer.Revision, transfer.Part)
		record.ID = transfer.ID
		value = transfer
	case "room_tombstone":
		value = data
	default:
		return record, fmt.Errorf("unsupported migration kind")
	}
	record.Data, err = json.Marshal(value)
	return record, err
}

func migrationHashes(options Options, plan *Plan, relative, expected string, size int64) (filehash.Digests, error) {
	if !filepath.IsLocal(relative) {
		return filehash.Digests{}, fmt.Errorf("source path must be relative")
	}
	filename := filepath.Join(options.FromDir, filepath.FromSlash(relative))
	physical, err := physicalPath(filename)
	if err != nil || physical != filename {
		return filehash.Digests{}, fmt.Errorf("migration source cannot contain path aliases: %s", filename)
	}
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() {
		return filehash.Digests{}, fmt.Errorf("migration source must be a regular file: %s", filename)
	}
	hashes, err := filehash.File(filename)
	if err != nil {
		return hashes, err
	}
	if hashes.SHA256 != expected || hashes.Size != size {
		return hashes, fmt.Errorf("source file checksum or size differs from database")
	}
	plan.SourceHashes[filename] = hashes.SHA256
	return hashes, nil
}

func TokenSettings(filename string) (string, error) {
	var legacy struct {
		Users map[string]string `toml:"users"`
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	if err = toml.Unmarshal(data, &legacy); err != nil {
		return "", fmt.Errorf("invalid legacy users file")
	}
	result := struct {
		Users map[string]config.User `toml:"users"`
	}{Users: map[string]config.User{}}
	for id, token := range legacy.Users {
		if !config.ValidID(id) || token == "" {
			return "", fmt.Errorf("invalid legacy user ID or empty token")
		}
		digest := sha256.Sum256([]byte(token))
		result.Users[id] = config.User{TokenSHA256: hex.EncodeToString(digest[:]), Identities: []string{"static:" + id}, Minecraft: []string{}}
	}
	data, err = toml.Marshal(result)
	return string(data), err
}

func databaseFingerprint(ctx context.Context, tx *sql.Tx) (string, error) {
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	for _, query := range []string{
		"SELECT kind,id,data FROM records ORDER BY kind,id",
		"SELECT 'counter',name,CAST(value AS TEXT) FROM counters ORDER BY name",
	} {
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var values [3]string
			if err = rows.Scan(&values[0], &values[1], &values[2]); err != nil {
				rows.Close()
				return "", err
			}
			if err = encoder.Encode(values); err != nil {
				rows.Close()
				return "", err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
