package auth

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func runSecretTool(token string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "secret-tool", args...)
	command.Stdin = strings.NewReader(token)
	output, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && len(exit.Stderr) == 0 {
			return "", errSystemCredentialNotFound
		}
		return "", fmt.Errorf("Secret Service unavailable")
	}
	return string(output), nil
}

func systemCredentialSet(server, token string) error {
	_, err := runSecretTool(token, "store", "--label=Vex CLI "+server, "service", clientKeyringService, "username", server)
	return err
}

func systemCredentialGet(server string) (string, error) {
	return runSecretTool("", "lookup", "service", clientKeyringService, "username", server)
}

func systemCredentialDelete(server string) error {
	_, err := runSecretTool("", "clear", "service", clientKeyringService, "username", server)
	return err
}
