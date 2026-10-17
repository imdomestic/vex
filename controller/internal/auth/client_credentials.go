package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const clientKeyringService = "com.imdomestic.vex.cli"

var ErrKeyringCleanupUnavailable = errors.New("local credential removed; system keyring cleanup unavailable")

var errSystemCredentialNotFound = errors.New("system credential not found")

type CredentialStore struct {
	Directory string
}

type credentialRecord struct {
	Backend string `json:"backend"`
	Token   string `json:"token,omitempty"`
}

func NormalizeServer(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid server URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, "\r\n\x00") {
		return "", fmt.Errorf("server requires an HTTP URL without credentials, query or fragment")
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	if u.Scheme == "http" && u.Port() == "80" || u.Scheme == "https" && u.Port() == "443" {
		u.Host = u.Hostname()
		if strings.Contains(u.Host, ":") {
			u.Host = "[" + u.Host + "]"
		}
	}
	if port := u.Port(); port != "" {
		if _, err := net.LookupPort("tcp", port); err != nil {
			return "", fmt.Errorf("server port is invalid")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

func (s CredentialStore) filename(server string) (string, error) {
	canonical, err := NormalizeServer(server)
	if err != nil || !filepath.IsAbs(s.Directory) {
		return "", fmt.Errorf("invalid credential storage location")
	}
	return filepath.Join(s.Directory, tokenDigest(canonical)+".json"), nil
}

func (s CredentialStore) Save(server, token string) (string, error) {
	filename, err := s.filename(server)
	if err != nil {
		return "", err
	}
	if token == "" || len(token) > 512 || strings.ContainsAny(token, "\r\n\x00") {
		return "", fmt.Errorf("invalid credential")
	}
	canonical, _ := NormalizeServer(server)
	record := credentialRecord{Backend: "keyring"}
	if err = systemCredentialSet(canonical, token); err != nil {
		record = credentialRecord{Backend: "file", Token: token}
	}
	data, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(s.Directory, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(s.Directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return "", fmt.Errorf("credential directory must have mode 0700")
	}
	var directoryInfo unix.Stat_t
	if err = unix.Lstat(s.Directory, &directoryInfo); err != nil || int(directoryInfo.Uid) != os.Geteuid() {
		return "", fmt.Errorf("credential directory must be owned by this user")
	}
	staging := filename + "." + uuid.NewString()
	file, err := os.OpenFile(staging, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer os.Remove(staging)
	_, writeErr := file.Write(data)
	if err = errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return "", err
	}
	if err = os.Rename(staging, filename); err != nil {
		return "", err
	}
	directory, err := os.Open(s.Directory)
	if err != nil {
		return "", err
	}
	if err = errors.Join(directory.Sync(), directory.Close()); err != nil {
		return "", err
	}
	return record.Backend, nil
}

func (s CredentialStore) Get(server string) (string, error) {
	filename, err := s.filename(server)
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(filename, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrUnauthenticated
	}
	if err != nil {
		return "", fmt.Errorf("cannot open local credential record")
	}
	defer file.Close()
	var info unix.Stat_t
	if err = unix.Fstat(int(file.Fd()), &info); err != nil || int(info.Uid) != os.Geteuid() || info.Mode&0777 != 0600 || info.Mode&unix.S_IFMT != unix.S_IFREG {
		return "", fmt.Errorf("credential file must be owned by this user with mode 0600")
	}
	var record credentialRecord
	decoder := json.NewDecoder(io.LimitReader(file, 8192))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&record); err != nil {
		return "", fmt.Errorf("invalid local credential record")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return "", fmt.Errorf("invalid trailing credential data")
	}
	switch record.Backend {
	case "file":
		if record.Token == "" || len(record.Token) > 512 || strings.ContainsAny(record.Token, "\r\n\x00") {
			return "", ErrUnauthenticated
		}
		return record.Token, nil
	case "keyring":
		canonical, _ := NormalizeServer(server)
		token, err := systemCredentialGet(canonical)
		if err != nil {
			return "", fmt.Errorf("cannot read system keyring; log in again")
		}
		return token, nil
	default:
		return "", fmt.Errorf("invalid credential backend")
	}
}

func (s CredentialStore) Delete(server string) error {
	filename, err := s.filename(server)
	if err != nil {
		return err
	}
	canonical, _ := NormalizeServer(server)
	keyringErr := systemCredentialDelete(canonical)
	fileErr := os.Remove(filename)
	if errors.Is(fileErr, os.ErrNotExist) {
		fileErr = nil
	}
	if fileErr != nil {
		return fmt.Errorf("cannot remove local credential record")
	}
	if keyringErr != nil && !errors.Is(keyringErr, errSystemCredentialNotFound) {
		return ErrKeyringCleanupUnavailable
	}
	return fileErr
}
