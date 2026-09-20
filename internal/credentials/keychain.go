package credentials

import (
	"errors"
	"fmt"
	"time"

	"github.com/zalando/go-keyring"
)

// Keychain abstracts the host keychain / secret service.
type Keychain interface {
	Name() string
	Available() bool
	Get(account string) (string, error)
	Set(account, secret string) error
	Delete(account string) error
}

var (
	ErrUnavailable = errors.New("no host keychain available")
	ErrNotFound    = errors.New("credential not found in keychain")
)

const keyringService = "signet"
const keyringTimeout = 5 * time.Second

// keyringBackend wraps zalando/go-keyring with timeouts.
type keyringBackend struct {
	service string
	reason  string // human-readable unavailable reason
}

// NewKeyringBackend creates a keyring-backed Keychain for the named service.
// The package default still uses "signet" so existing callers are unchanged.
func NewKeyringBackend(service string) Keychain {
	return &keyringBackend{service: service}
}

func newKeyringBackend() Keychain {
	return NewKeyringBackend(keyringService)
}

func (k *keyringBackend) Name() string { return "keychain" }

func (k *keyringBackend) Available() bool {
	_, err := k.Get(k.service + ":available-probe")
	if err == nil || errors.Is(err, ErrNotFound) {
		return true
	}
	k.reason = err.Error()
	return false
}

func (k *keyringBackend) reasonText() string {
	if k.reason != "" {
		return k.reason
	}
	return "no Secret Service on this session bus"
}

func (k *keyringBackend) Get(account string) (string, error) {
	ch := make(chan struct {
		val string
		err error
	}, 1)
	go func() {
		v, err := keyring.Get(k.service, account)
		if errors.Is(err, keyring.ErrNotFound) {
			ch <- struct {
				val string
				err error
			}{"", ErrNotFound}
			return
		}
		ch <- struct {
			val string
			err error
		}{v, err}
	}()
	select {
	case res := <-ch:
		return res.val, res.err
	case <-time.After(keyringTimeout):
		return "", fmt.Errorf("%w: keychain get timed out", ErrUnavailable)
	}
}

func (k *keyringBackend) Set(account, secret string) error {
	ch := make(chan error, 1)
	go func() {
		ch <- keyring.Set(k.service, account, secret)
	}()
	select {
	case err := <-ch:
		return err
	case <-time.After(keyringTimeout):
		return fmt.Errorf("%w: keychain set timed out", ErrUnavailable)
	}
}

func (k *keyringBackend) Delete(account string) error {
	ch := make(chan error, 1)
	go func() {
		ch <- keyring.Delete(k.service, account)
	}()
	select {
	case err := <-ch:
		return err
	case <-time.After(keyringTimeout):
		return fmt.Errorf("%w: keychain delete timed out", ErrUnavailable)
	}
}

// fakeKeychain is a test seam.
type fakeKeychain struct {
	data   map[string]string
	sleep  time.Duration
	broken bool
}

func (f *fakeKeychain) Name() string    { return "fake-keychain" }
func (f *fakeKeychain) Available() bool { return !f.broken }
func (f *fakeKeychain) Get(account string) (string, error) {
	if f.sleep > 0 {
		time.Sleep(f.sleep)
	}
	if f.broken {
		return "", ErrUnavailable
	}
	v, ok := f.data[account]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}
func (f *fakeKeychain) Set(account, secret string) error {
	if f.broken {
		return ErrUnavailable
	}
	if f.data == nil {
		f.data = map[string]string{}
	}
	f.data[account] = secret
	return nil
}
func (f *fakeKeychain) Delete(account string) error {
	if f.broken {
		return ErrUnavailable
	}
	delete(f.data, account)
	return nil
}
