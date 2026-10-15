package main

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/imdomestic/chorus-testbed/controller/internal/model"
)

type streamUpload struct {
	File          *os.File
	Reader        io.Reader
	ContentType   string
	ContentLength int64
	Verify        func([]byte) error
}

func publicationCommand(args []string) (method, path string, body any, upload *streamUpload, err error) {
	if len(args) == 2 && args[0] == "asset" && args[1] == "list" {
		return "GET", "/v1/assets", nil, nil, nil
	}
	if len(args) < 3 {
		return "", "", nil, nil, fmt.Errorf("usage: asset list|publish ID --world FILE --metadata FILE|promote ID --edit SNAPSHOT|archive ID|restore ID; pack publish ID --file ZIP")
	}
	kind, action, id := args[0], args[1], args[2]
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\:\r\n\x00") {
		return "", "", nil, nil, fmt.Errorf("invalid publication ID")
	}
	path = "/v1/" + kind + "s/" + url.PathEscape(id)
	if kind == "pack" {
		if action != "publish" {
			return "", "", nil, nil, fmt.Errorf("pack supports publish")
		}
		flags := flag.NewFlagSet("pack publish", flag.ContinueOnError)
		archive := flags.String("file", "", "resource pack ZIP")
		if err = flags.Parse(args[3:]); err != nil {
			return
		}
		if flags.NArg() != 0 || *archive == "" {
			err = fmt.Errorf("pack publish requires --file ZIP")
			return
		}
		upload, err = packUpload(id, *archive)
		return "POST", path + "/revisions", nil, upload, err
	}
	switch action {
	case "publish":
		flags := flag.NewFlagSet("asset publish", flag.ContinueOnError)
		world := flags.String("world", "", "world tar.gz")
		metadata := flags.String("metadata", "", "AssetMetadata JSON")
		if err = flags.Parse(args[3:]); err != nil {
			return
		}
		if flags.NArg() != 0 || *world == "" || *metadata == "" {
			err = fmt.Errorf("asset publish requires --world FILE and --metadata FILE")
			return
		}
		upload, err = assetUpload(id, *world, *metadata)
		return "POST", path + "/revisions", nil, upload, err
	case "promote":
		flags := flag.NewFlagSet("asset promote", flag.ContinueOnError)
		edit := flags.String("edit", "", "asset editing snapshot ID")
		if err = flags.Parse(args[3:]); err != nil {
			return
		}
		if flags.NArg() != 0 || *edit == "" {
			err = fmt.Errorf("asset promote requires --edit SNAPSHOT")
			return
		}
		return "POST", path + "/promote", map[string]string{"edit": *edit}, nil, nil
	case "archive":
		if len(args) != 3 {
			err = fmt.Errorf("asset archive requires one ID")
			return
		}
		return "PATCH", path, map[string]string{"state": "archived"}, nil, nil
	case "restore":
		if len(args) != 3 {
			err = fmt.Errorf("asset restore requires one ID")
			return
		}
		return "POST", path + "/restore", nil, nil, nil
	default:
		return "", "", nil, nil, fmt.Errorf("unknown asset operation %q", action)
	}
}

func publicationFile(name string) (*os.File, error) {
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return nil, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, err
	}
	const protected = "/home/linwhite/d2-assets/sensitive"
	if resolved == protected || strings.HasPrefix(resolved, protected+string(filepath.Separator)) {
		return nil, fmt.Errorf("禁止读取敏感资源目录")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		file.Close()
		return nil, fmt.Errorf("publication input must be a nonempty regular file")
	}
	return file, nil
}

func assetUpload(id, worldPath, metadataPath string) (*streamUpload, error) {
	metadataFile, err := publicationFile(metadataPath)
	if err != nil {
		return nil, err
	}
	defer metadataFile.Close()
	metadataBytes, err := io.ReadAll(io.LimitReader(metadataFile, 1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(metadataBytes) > 1024*1024 {
		return nil, fmt.Errorf("asset metadata exceeds 1 MiB")
	}
	var metadata model.AssetMetadata
	decoder := json.NewDecoder(bytes.NewReader(metadataBytes))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&metadata); err != nil {
		return nil, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("asset metadata contains extra JSON")
	}
	if metadata.ID != id {
		return nil, fmt.Errorf("asset metadata ID differs from command ID")
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
	part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="asset"`}, "Content-Type": {"application/json"}})
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
	digest := sha256.New()
	reader := io.MultiReader(bytes.NewReader(envelope.Bytes()[:prefixLength]), io.TeeReader(file, digest), bytes.NewReader(envelope.Bytes()[prefixLength:]))
	return &streamUpload{File: file, Reader: reader, ContentType: writer.FormDataContentType(), ContentLength: int64(envelope.Len()) + info.Size(), Verify: func(data []byte) error {
		var revision model.AssetRevision
		if err := json.Unmarshal(data, &revision); err != nil {
			return err
		}
		if revision.ID != id || revision.World.Size != info.Size() || revision.World.SHA256 != hex.EncodeToString(digest.Sum(nil)) {
			return fmt.Errorf("published world ID, SHA-256 or size differs from uploaded bytes")
		}
		return nil
	}}, nil
}

func packUpload(id, archivePath string) (*streamUpload, error) {
	file, err := publicationFile(archivePath)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	digest, legacyDigest := sha256.New(), sha1.New()
	return &streamUpload{File: file, Reader: io.TeeReader(file, io.MultiWriter(digest, legacyDigest)), ContentType: "application/zip", ContentLength: info.Size(), Verify: func(data []byte) error {
		var revision model.PackRevision
		if err := json.Unmarshal(data, &revision); err != nil {
			return err
		}
		checksum := hex.EncodeToString(digest.Sum(nil))
		if revision.ID != id || revision.Size != info.Size() || revision.SHA256 != checksum || revision.Rev != checksum || revision.SHA1 != hex.EncodeToString(legacyDigest.Sum(nil)) {
			return fmt.Errorf("published resource pack ID, hashes or size differ from uploaded bytes")
		}
		return nil
	}}, nil
}

func responseError(status int, data []byte) error {
	var response struct {
		Error *model.Error `json:"error"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return fmt.Errorf("HTTP %d returned a non-JSON error: %s", status, strings.TrimSpace(string(data)))
	}
	if response.Error == nil {
		return fmt.Errorf("HTTP %d returned an error without an error object", status)
	}
	return fmt.Errorf("HTTP %d %s: %s", status, response.Error.Code, response.Error.Message)
}
