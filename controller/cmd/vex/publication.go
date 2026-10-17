package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/imdomestic/vex/controller/internal/config"
	"github.com/imdomestic/vex/controller/internal/model"
	"github.com/spf13/pflag"
)

type streamUpload struct {
	File          *os.File
	Reader        io.Reader
	ContentType   string
	ContentLength int64
	Verify        func([]byte) error
	Temporary     string
}

func (u *streamUpload) Close() error {
	err := u.File.Close()
	if u.Temporary != "" {
		err = errors.Join(err, os.Remove(u.Temporary))
	}
	return err
}

func publicationCommand(ctx context.Context, global *pflag.FlagSet, args []string) (requestPlan, config.Resolved[config.Client], error) {
	plan := requestPlan{Method: http.MethodGet}
	var resolved config.Resolved[config.Client]
	flags := commandFlags(global, args[0])
	world := flags.String("world", "", "world ID")
	pack := flags.String("pack", "", "pack ID or world resource pack ID@revision")
	title := flags.String("title", "", "world title")
	metadata := flags.String("metadata", "", "world metadata JSON file")
	work := flags.String("work-dir", ".work/vex", "temporary publication directory")
	if err := parse(flags, args[1:]); err != nil {
		return plan, resolved, err
	}
	var err error
	resolved, err = resolveClient(global)
	if err != nil {
		return plan, resolved, err
	}
	if err = selectClient(global, &resolved); err != nil {
		return plan, resolved, err
	}
	if err = selectProject(ctx, &resolved.Config); err != nil {
		return plan, resolved, err
	}
	prefix, err := projectPrefix(resolved.Config)
	if err != nil {
		return plan, resolved, err
	}
	positionals := flags.Args()
	if len(positionals) == 1 && positionals[0] == "list" {
		plan.Path = prefix + "/" + args[0] + "s"
		return plan, resolved, nil
	}
	if len(positionals) != 2 {
		return plan, resolved, fmt.Errorf("usage: world publish PATH --world ID [--title TITLE] [--pack ID@REV] | world show|archive|restore ID | world promote SNAPSHOT; pack publish ZIP --pack ID")
	}
	action, input := positionals[0], positionals[1]
	if args[0] == "pack" {
		if action != "publish" || !config.ValidID(*pack) {
			return plan, resolved, fmt.Errorf("pack publish requires ZIP and --pack ID")
		}
		plan.Method, plan.Path = http.MethodPost, prefix+"/packs/"+*pack+"/revisions"
		plan.Upload, err = packUpload(resolved.Config.Project, *pack, input)
		return plan, resolved, err
	}
	if action == "show" || action == "archive" || action == "restore" {
		if !config.ValidID(input) {
			return plan, resolved, fmt.Errorf("invalid world ID")
		}
		plan.Path = prefix + "/worlds/" + input
		if action == "archive" {
			plan.Method, plan.Body = http.MethodPatch, map[string]string{"state": "archived"}
		} else if action == "restore" {
			plan.Method, plan.Path = http.MethodPost, plan.Path+"/restore"
		}
		return plan, resolved, nil
	}
	c, err := newClient(resolved.Config, false)
	if err != nil {
		return plan, resolved, err
	}
	if action == "promote" {
		var snapshot model.Snapshot
		if err = c.get(ctx, prefix+"/snapshots/"+url.PathEscape(input), &snapshot); err != nil {
			return plan, resolved, err
		}
		if snapshot.Project != resolved.Config.Project || snapshot.World == nil || !config.ValidID(snapshot.World.ID) {
			return plan, resolved, fmt.Errorf("snapshot has no world source in the selected project")
		}
		plan.Method, plan.Path = http.MethodPost, prefix+"/worlds/"+snapshot.World.ID+"/promote"
		plan.Body = map[string]string{"snapshot": input}
		return plan, resolved, nil
	}
	if action != "publish" || !config.ValidID(*world) {
		return plan, resolved, fmt.Errorf("world publish requires PATH and --world ID")
	}
	var project model.Project
	if err = c.get(ctx, prefix, &project); err != nil {
		return plan, resolved, err
	}
	if project.ID != resolved.Config.Project || project.Minecraft == "" || len(project.Loaders) == 0 {
		return plan, resolved, fmt.Errorf("project response lacks world compatibility settings")
	}
	value := model.WorldMetadata{
		ID: *world, Title: *world, Minecraft: project.Minecraft,
		Requires: model.Requirements{Loaders: project.Loaders, Mods: []string{}}, Source: model.Source{Scale: 1},
	}
	if *metadata != "" {
		if err = readMetadata(*metadata, &value); err != nil {
			return plan, resolved, err
		}
		if value.ID != *world {
			return plan, resolved, fmt.Errorf("metadata ID differs from --world")
		}
	}
	if flags.Changed("title") {
		value.Title = *title
	}
	if *pack != "" {
		id, rev, ok := strings.Cut(*pack, "@")
		digest, err := hex.DecodeString(rev)
		if !ok || !config.ValidID(id) || err != nil || len(digest) != 32 || rev != strings.ToLower(rev) {
			return plan, resolved, fmt.Errorf("resource pack requires ID@SHA256")
		}
		value.ResourcePack = &model.PackRef{ID: id, Rev: rev}
	}
	info, err := os.Stat(input)
	if err != nil {
		return plan, resolved, err
	}
	temporary := ""
	if info.IsDir() {
		temporary, err = directoryArchive(input, *work)
		if err != nil {
			return plan, resolved, err
		}
		input = temporary
	}
	plan.Upload, err = worldUpload(resolved.Config.Project, value, input)
	if err != nil {
		if temporary != "" {
			err = errors.Join(err, os.Remove(temporary))
		}
		return plan, resolved, err
	}
	plan.Upload.Temporary = temporary
	plan.Method, plan.Path = http.MethodPost, prefix+"/worlds/"+*world+"/revisions"
	return plan, resolved, nil
}

func publicationFile(name string) (*os.File, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("publication input must be a nonempty regular file")
	}
	return file, nil
}

func readMetadata(path string, value *model.WorldMetadata) error {
	file, err := publicationFile(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1024*1024+1))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(value); err != nil {
		return fmt.Errorf("invalid world metadata JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fmt.Errorf("world metadata contains extra JSON")
	}
	return nil
}

func worldUpload(project string, metadata model.WorldMetadata, worldPath string) (*streamUpload, error) {
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	file, err := publicationFile(worldPath)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*streamUpload, error) { file.Close(); return nil, err }
	info, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	var envelope bytes.Buffer
	writer := multipart.NewWriter(&envelope)
	part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="metadata"`}, "Content-Type": {"application/json"}})
	if err != nil {
		return fail(err)
	}
	if _, err = part.Write(metadataBytes); err != nil {
		return fail(err)
	}
	if _, err = writer.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="world"; filename="world.tar.gz"`}, "Content-Type": {"application/gzip"}}); err != nil {
		return fail(err)
	}
	prefixLength := envelope.Len()
	if err = writer.Close(); err != nil {
		return fail(err)
	}
	one, two, five := sha1.New(), sha256.New(), sha512.New()
	reader := io.MultiReader(bytes.NewReader(envelope.Bytes()[:prefixLength]), io.TeeReader(file, io.MultiWriter(one, two, five)), bytes.NewReader(envelope.Bytes()[prefixLength:]))
	return &streamUpload{File: file, Reader: reader, ContentType: writer.FormDataContentType(), ContentLength: int64(envelope.Len()) + info.Size(), Verify: func(data []byte) error {
		var revision model.WorldRevision
		if err := json.Unmarshal(data, &revision); err != nil {
			return err
		}
		if revision.Project != project || revision.ID != metadata.ID || revision.World.Size != info.Size() || revision.World.SHA1 != hex.EncodeToString(one.Sum(nil)) || revision.World.SHA256 != hex.EncodeToString(two.Sum(nil)) || revision.World.SHA512 != hex.EncodeToString(five.Sum(nil)) {
			return fmt.Errorf("published world identity, hashes or size differ from uploaded bytes")
		}
		return nil
	}}, nil
}

func packUpload(project, id, path string) (*streamUpload, error) {
	file, err := publicationFile(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	one, two, five := sha1.New(), sha256.New(), sha512.New()
	return &streamUpload{File: file, Reader: io.TeeReader(file, io.MultiWriter(one, two, five)), ContentType: "application/zip", ContentLength: info.Size(), Verify: func(data []byte) error {
		var revision model.PackRevision
		if err := json.Unmarshal(data, &revision); err != nil {
			return err
		}
		checksum := hex.EncodeToString(two.Sum(nil))
		if revision.Project != project || revision.ID != id || revision.Size != info.Size() || revision.SHA256 != checksum || revision.Rev != checksum || revision.SHA1 != hex.EncodeToString(one.Sum(nil)) || revision.SHA512 != hex.EncodeToString(five.Sum(nil)) {
			return fmt.Errorf("published resource pack identity, hashes or size differ from uploaded bytes")
		}
		return nil
	}}, nil
}

func directoryArchive(source, scratch string) (result string, resultErr error) {
	root, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	scratch, err = filepath.Abs(scratch)
	if err != nil {
		return "", err
	}
	scratch, err = physicalDirectory(scratch)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, scratch)
	if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return "", fmt.Errorf("--work-dir must be outside the published world directory")
	}
	if err = os.MkdirAll(scratch, 0700); err != nil {
		return "", err
	}
	sourceRoot, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer sourceRoot.Close()
	file, err := os.CreateTemp(scratch, "world-*.tar.gz")
	if err != nil {
		return "", err
	}
	defer func() {
		if resultErr != nil {
			file.Close()
			resultErr = errors.Join(resultErr, os.Remove(file.Name()))
		}
	}()
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	observed := map[string]os.FileInfo{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("world contains a link or special file")
		}
		observed[path] = info
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := "world"
		if relative != "." {
			name += "/" + filepath.ToSlash(relative)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name, header.Uid, header.Gid, header.Uname, header.Gname = name, 0, 0, "", ""
		header.ModTime, header.AccessTime, header.ChangeTime = time.Time{}, time.Time{}, time.Time{}
		header.Mode, header.Format = 0600, tar.FormatPAX
		if info.IsDir() {
			header.Mode = 0700
		}
		if err = tarWriter.WriteHeader(header); err != nil || info.IsDir() {
			return err
		}
		input, err := sourceRoot.Open(relative)
		if err != nil {
			return err
		}
		opened, err := input.Stat()
		if err != nil {
			input.Close()
			return err
		}
		if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
			input.Close()
			return fmt.Errorf("world changed while opening an archive member")
		}
		_, copyErr := io.Copy(tarWriter, input)
		return errors.Join(copyErr, input.Close())
	})
	if err != nil {
		return "", err
	}
	for path, previous := range observed {
		current, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !os.SameFile(previous, current) || previous.Size() != current.Size() || !previous.ModTime().Equal(current.ModTime()) {
			return "", fmt.Errorf("world changed while creating the archive")
		}
	}
	if err = tarWriter.Close(); err != nil {
		return "", err
	}
	if err = gzipWriter.Close(); err != nil {
		return "", err
	}
	if err = file.Sync(); err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	return file.Name(), nil
}

func physicalDirectory(path string) (string, error) {
	missing := []string{}
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(path) == path {
			return "", err
		}
		missing = append(missing, filepath.Base(path))
		path = filepath.Dir(path)
	}
}
