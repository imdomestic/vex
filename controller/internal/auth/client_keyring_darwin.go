package auth

import (
	"errors"

	"github.com/zalando/go-keyring"
)

func systemCredentialSet(server, token string) error {
	return keyring.Set(clientKeyringService, server, token)
}

func systemCredentialGet(server string) (string, error) {
	return keyring.Get(clientKeyringService, server)
}

func systemCredentialDelete(server string) error {
	err := keyring.Delete(clientKeyringService, server)
	if errors.Is(err, keyring.ErrNotFound) {
		return errSystemCredentialNotFound
	}
	return err
}
