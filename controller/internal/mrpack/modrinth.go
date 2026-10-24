package mrpack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/imdomestic/vex/controller/internal/model"
)

func trustedCDN(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "cdn.modrinth.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(u.Path, "\\\x00") {
		return false
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "data" || parts[3] != "versions" || !filenamePattern.MatchString(parts[5]) || strings.Contains(parts[5], "..") {
		return false
	}
	for _, value := range []string{parts[2], parts[4]} {
		if !versionPattern.MatchString(value) || strings.Contains(value, "..") {
			return false
		}
	}
	return true
}

func verifiedDownload(ctx context.Context, file model.BuildFile) (string, error) {
	if !validHash(file.SHA512, 64) || !trustedCDN(file.SourceURL) {
		return "", fmt.Errorf("invalid public dependency provenance")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.modrinth.com/v2/version_file/"+file.SHA512+"?algorithm=sha512", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "imdomestic/vex (mrpack export)")
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("dependency metadata redirect is forbidden")
	}}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("public dependency metadata request failed")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("public dependency metadata returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20+1))
	if err != nil || len(data) > 2<<20 {
		return "", fmt.Errorf("public dependency metadata exceeds its limit")
	}
	var version struct {
		Files []struct {
			URL    string            `json:"url"`
			Size   int64             `json:"size"`
			Hashes map[string]string `json:"hashes"`
		} `json:"files"`
	}
	if err = json.Unmarshal(data, &version); err != nil {
		return "", fmt.Errorf("public dependency metadata is invalid")
	}
	for _, candidate := range version.Files {
		if candidate.URL == file.SourceURL && trustedCDN(candidate.URL) && candidate.Size == file.Size && candidate.Hashes["sha1"] == file.SHA1 && candidate.Hashes["sha512"] == file.SHA512 {
			return candidate.URL, nil
		}
	}
	return "", fmt.Errorf("public dependency provenance differs from verified file")
}
