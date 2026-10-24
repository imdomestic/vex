package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/imdomestic/vex/controller/internal/auth"
)

func deviceVerification(challenge auth.DeviceChallenge, now time.Time) (string, error) {
	if challenge.Handle == "" || challenge.UserCode == "" || challenge.VerificationURI == "" || len(challenge.UserCode) > 64 || strings.IndexFunc(challenge.UserCode, unicode.IsControl) >= 0 || challenge.Interval < 1 || challenge.Interval > 60 || !challenge.ExpiresAt.After(now) {
		return "", fmt.Errorf("invalid device authorization response")
	}
	parsed, err := url.Parse(challenge.VerificationURI)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.Path != "/login/device" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return "", fmt.Errorf("invalid device verification URL")
	}
	return challenge.VerificationURI, nil
}

func devicePending(body io.Reader) (time.Duration, error) {
	var pending struct {
		Status   string `json:"status"`
		Interval int64  `json:"interval"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 1024*1024)).Decode(&pending); err != nil || pending.Status != "pending" || pending.Interval < 1 || pending.Interval > 60 {
		return 0, fmt.Errorf("invalid device polling response")
	}
	return time.Duration(pending.Interval) * time.Second, nil
}

func (c *client) githubLogin(ctx context.Context, name string, output io.Writer) (auth.Login, error) {
	response, err := c.do(ctx, requestPlan{Method: http.MethodPost, Path: "/v2/auth/device", Body: map[string]string{}})
	if err != nil {
		return auth.Login{}, err
	}
	var challenge auth.DeviceChallenge
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&challenge)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil {
		return auth.Login{}, fmt.Errorf("invalid device authorization response")
	}
	verification, err := deviceVerification(challenge, time.Now())
	if err != nil {
		return auth.Login{}, err
	}
	if _, err = fmt.Fprintf(output, "Open %s and enter code %s.\nWaiting for authorization...\n", verification, challenge.UserCode); err != nil {
		return auth.Login{}, err
	}
	pollContext, cancel := context.WithDeadline(ctx, challenge.ExpiresAt)
	defer cancel()
	interval := time.Duration(challenge.Interval) * time.Second
	for {
		timer := time.NewTimer(interval)
		select {
		case <-pollContext.Done():
			timer.Stop()
			return auth.Login{}, pollContext.Err()
		case <-timer.C:
		}
		response, err = c.do(pollContext, requestPlan{Method: http.MethodPost, Path: "/v2/auth/device/poll", Body: map[string]string{"handle": challenge.Handle, "client": name}})
		if err != nil {
			return auth.Login{}, err
		}
		if response.StatusCode == http.StatusAccepted {
			interval, err = devicePending(response.Body)
			response.Body.Close()
			if err != nil {
				return auth.Login{}, err
			}
			continue
		}
		var result auth.Login
		decodeErr = json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&result)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || decodeErr != nil || result.Token == "" || result.User.ID == "" || !result.ExpiresAt.After(time.Now()) {
			return auth.Login{}, fmt.Errorf("invalid login response")
		}
		return result, nil
	}
}
