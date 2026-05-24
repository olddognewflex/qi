package calendar

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

const caldavKeychainService = "qi-caldav"

var ErrSecretNotFound = errors.New("secret not found in keychain")

func GetCalDAVPassword(name string) (string, error) {
	pw, err := keyring.Get(caldavKeychainService, name)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrSecretNotFound
		}
		return "", fmt.Errorf("keychain get %s/%s: %w", caldavKeychainService, name, err)
	}
	return pw, nil
}

func SetCalDAVPassword(name, password string) error {
	if err := keyring.Set(caldavKeychainService, name, password); err != nil {
		return fmt.Errorf("keychain set %s/%s: %w", caldavKeychainService, name, err)
	}
	return nil
}

func DeleteCalDAVPassword(name string) error {
	err := keyring.Delete(caldavKeychainService, name)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("keychain delete %s/%s: %w", caldavKeychainService, name, err)
	}
	return nil
}
