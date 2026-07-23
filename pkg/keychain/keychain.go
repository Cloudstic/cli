package keychain

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/store"
)

// noMatchingSlotErr builds the error a Credential.Resolve returns when its
// slots exist but none could be unwrapped. It joins each slot's specific
// failure (wrong password, corrupt slot, unreachable KMS, ...) instead of one
// generic message that looks identical regardless of cause.
func noMatchingSlotErr(kind string, errs []error) error {
	if len(errs) == 0 {
		return fmt.Errorf("no %s key slot found", kind)
	}
	return fmt.Errorf("no compatible %s key slot found: %w", kind, errors.Join(errs...))
}

// DeriveEncryptionKey derives the AES-256 encryption key from a master key.
func DeriveEncryptionKey(masterKey []byte) ([]byte, error) {
	return crypto.DeriveKey(masterKey, crypto.HKDFInfoBackupV1)
}

// AddRecoverySlot generates a recovery key, wraps the given master key with
// it, stores the recovery slot under the given label, and returns the BIP39
// 24-word mnemonic.
//
// A recovery mnemonic is a last-resort credential, so an existing slot with the
// same label is never replaced silently: overwriting it invalidates the
// mnemonic the user wrote down. Callers must pass replace=true to do so
// deliberately, or use a different label to add a second recovery key that
// leaves the first one working.
func AddRecoverySlot(ctx context.Context, s store.ObjectStore, masterKey []byte, label string, replace bool) (mnemonic string, err error) {
	label, err = NormalizeSlotLabel(label)
	if err != nil {
		return "", err
	}
	if !replace {
		exists, err := s.Exists(ctx, slotObjectKey(SlotTypeRecovery, label))
		if err != nil {
			return "", fmt.Errorf("check existing recovery key slot: %w", err)
		}
		if exists {
			return "", &SlotExistsError{SlotType: SlotTypeRecovery, Label: label}
		}
	}
	mnemonic, recoveryKey, err := crypto.GenerateRecoveryMnemonic()
	if err != nil {
		return "", err
	}
	slot, err := CreateRecoverySlot(masterKey, recoveryKey)
	if err != nil {
		return "", err
	}
	slot.Label = label
	if err := WriteKeySlot(ctx, s, slot); err != nil {
		return "", err
	}
	return mnemonic, nil
}

// ChangePasswordSlot replaces (or creates) the password key slot for the
// repository. masterKey is the unwrapped master key; newPassword is the new
// password to wrap it with. The old password slot (keys/password-default) is
// overwritten.
func ChangePasswordSlot(ctx context.Context, s store.ObjectStore, masterKey []byte, newPassword string) error {
	if newPassword == "" {
		return fmt.Errorf("new password cannot be empty")
	}
	slot, err := CreatePasswordSlot(masterKey, newPassword)
	if err != nil {
		return err
	}
	return WriteKeySlot(ctx, s, slot)
}

// Credential attempts to resolve or wrap the master key for the repository.
type Credential interface {
	// Resolve attempts to derive the master key from the given key slots.
	// Returns the master key if successful, or an error otherwise.
	Resolve(ctx context.Context, slots []KeySlot) ([]byte, error)

	// Wrap generates a new KeySlot wrapping the given master key.
	// Returns the created KeySlot, or an error if the credential cannot wrap.
	Wrap(ctx context.Context, masterKey []byte) (KeySlot, error)
}

// Chain is an ordered collection of Credentials.
type Chain []Credential

// Resolve attempts to resolve the master key by trying the given resolvers in order.
// It returns the first successfully retrieved master key.
func (c Chain) Resolve(ctx context.Context, slots []KeySlot) ([]byte, error) {
	if len(slots) == 0 {
		return nil, fmt.Errorf("repository is encrypted but no key slots found")
	}
	if len(c) == 0 {
		return nil, fmt.Errorf("repository is encrypted: no resolvers configured in the keychain")
	}

	var errs []error
	for _, resolver := range c {
		mk, err := resolver.Resolve(ctx, slots)
		if err == nil && len(mk) > 0 {
			return mk, nil
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("repository is encrypted: no provided credential matches the stored key slots (types: %s)", SlotTypes(slots))
	}
	return nil, fmt.Errorf("repository is encrypted: no provided credential matches the stored key slots (types: %s): %w",
		SlotTypes(slots), errors.Join(errs...))
}

// WrapAll attempts to wrap the master key using all configured credentials in the chain.
// It returns a slice of generated key slots. It ignores credentials that return ErrCannotWrap.
func (c Chain) WrapAll(ctx context.Context, masterKey []byte) ([]KeySlot, error) {
	var results []KeySlot
	for _, cred := range c {
		slot, err := cred.Wrap(ctx, masterKey)
		if err == ErrCannotWrap {
			continue
		}
		if err != nil {
			return nil, err
		}
		results = append(results, slot)
	}
	return results, nil
}

// ErrCannotWrap indicates a Credential does not support wrapping.
var ErrCannotWrap = fmt.Errorf("credential does not support wrapping")

// unwrapMasterKey tries to unwrap the master key from a slot using the given
// wrapping key. Returns the master key on success.
func unwrapMasterKey(slot KeySlot, wrappingKey []byte) ([]byte, error) {
	wrapped, err := base64.StdEncoding.DecodeString(slot.WrappedKey)
	if err != nil {
		return nil, fmt.Errorf("decode wrapped key: %w", err)
	}
	return crypto.UnwrapKey(wrapped, wrappingKey)
}

func CreatePlatformSlot(masterKey, platformKey []byte) (KeySlot, error) {
	wrapped, err := crypto.WrapKey(masterKey, platformKey)
	if err != nil {
		return KeySlot{}, fmt.Errorf("wrap master key with platform key: %w", err)
	}
	return KeySlot{
		SlotType:   SlotTypePlatform,
		WrappedKey: base64.StdEncoding.EncodeToString(wrapped),
		Label:      DefaultSlotLabel,
	}, nil
}

func CreatePasswordSlot(masterKey []byte, password string) (KeySlot, error) {
	salt := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return KeySlot{}, fmt.Errorf("generate salt: %w", err)
	}
	params := crypto.DefaultArgon2Params
	wrappingKey := crypto.DeriveKeyFromPassword(password, salt, params)
	wrapped, err := crypto.WrapKey(masterKey, wrappingKey)
	if err != nil {
		return KeySlot{}, fmt.Errorf("wrap master key with password: %w", err)
	}
	return KeySlot{
		SlotType:   SlotTypePassword,
		WrappedKey: base64.StdEncoding.EncodeToString(wrapped),
		Label:      DefaultSlotLabel,
		KDFParams: &KDFParams{
			Algorithm: "argon2id",
			Salt:      base64.StdEncoding.EncodeToString(salt),
			Time:      params.Time,
			Memory:    params.Memory,
			Threads:   params.Threads,
		},
	}, nil
}

func CreateRecoverySlot(masterKey, recoveryKey []byte) (KeySlot, error) {
	wrapped, err := crypto.WrapKey(masterKey, recoveryKey)
	if err != nil {
		return KeySlot{}, fmt.Errorf("wrap master key with recovery key: %w", err)
	}
	return KeySlot{
		SlotType:   SlotTypeRecovery,
		WrappedKey: base64.StdEncoding.EncodeToString(wrapped),
		Label:      DefaultSlotLabel,
	}, nil
}

type platformKeyCred struct {
	key []byte
}

// WithPlatformKey returns a credential using a raw platform key.
func WithPlatformKey(key []byte) Credential {
	return platformKeyCred{key: key}
}

func (c platformKeyCred) Resolve(ctx context.Context, slots []KeySlot) ([]byte, error) {
	if len(c.key) == 0 {
		return nil, fmt.Errorf("empty platform key")
	}
	var errs []error
	for _, slot := range slots {
		if slot.SlotType != "platform" {
			continue
		}
		mk, err := unwrapMasterKey(slot, c.key)
		if err == nil {
			return mk, nil
		}
		errs = append(errs, fmt.Errorf("slot %q: %w", slot.Label, err))
	}
	return nil, noMatchingSlotErr("platform", errs)
}

func (c platformKeyCred) Wrap(ctx context.Context, masterKey []byte) (KeySlot, error) {
	if len(c.key) == 0 {
		return KeySlot{}, fmt.Errorf("empty platform key")
	}
	return CreatePlatformSlot(masterKey, c.key)
}

type passwordCred struct {
	password string
}

// WithPassword returns a credential using a password.
func WithPassword(password string) Credential {
	return passwordCred{password: password}
}

func (c passwordCred) Resolve(ctx context.Context, slots []KeySlot) ([]byte, error) {
	if c.password == "" {
		return nil, fmt.Errorf("empty password")
	}
	var errs []error
	for _, slot := range slots {
		if slot.SlotType != "password" {
			continue
		}
		if slot.KDFParams == nil {
			errs = append(errs, fmt.Errorf("slot %q: missing KDF params", slot.Label))
			continue
		}
		salt, err := base64.StdEncoding.DecodeString(slot.KDFParams.Salt)
		if err != nil {
			errs = append(errs, fmt.Errorf("slot %q: decode salt: %w", slot.Label, err))
			continue
		}
		wrappingKey := crypto.DeriveKeyFromPassword(c.password, salt, crypto.Argon2Params{
			Time:    slot.KDFParams.Time,
			Memory:  slot.KDFParams.Memory,
			Threads: slot.KDFParams.Threads,
		})
		mk, err := unwrapMasterKey(slot, wrappingKey)
		if err == nil {
			return mk, nil
		}
		errs = append(errs, fmt.Errorf("slot %q: %w", slot.Label, err))
	}
	return nil, noMatchingSlotErr("password", errs)
}

func (c passwordCred) Wrap(ctx context.Context, masterKey []byte) (KeySlot, error) {
	return CreatePasswordSlot(masterKey, c.password)
}

type recoveryCred struct {
	mnemonic string
}

// WithRecoveryKey returns a credential using a BIP39 recovery mnemonic.
func WithRecoveryKey(mnemonic string) Credential {
	return recoveryCred{mnemonic: mnemonic}
}

func (c recoveryCred) Resolve(ctx context.Context, slots []KeySlot) ([]byte, error) {
	if c.mnemonic == "" {
		return nil, fmt.Errorf("empty mnemonic")
	}
	recoveryKey, err := crypto.MnemonicToKey(c.mnemonic)
	if err != nil {
		return nil, fmt.Errorf("invalid recovery key mnemonic: %w", err)
	}
	var errs []error
	for _, slot := range slots {
		if slot.SlotType != "recovery" {
			continue
		}
		mk, err := unwrapMasterKey(slot, recoveryKey)
		if err == nil {
			return mk, nil
		}
		errs = append(errs, fmt.Errorf("slot %q: %w", slot.Label, err))
	}
	return nil, noMatchingSlotErr("recovery", errs)
}

func (c recoveryCred) Wrap(ctx context.Context, masterKey []byte) (KeySlot, error) {
	return KeySlot{}, ErrCannotWrap
}

type kmsClientCred struct {
	client crypto.KMSClient
}

// WithKMSClient returns a credential using an explicit AWS KMS client.
func WithKMSClient(client crypto.KMSClient) Credential {
	return kmsClientCred{client: client}
}

func (c kmsClientCred) Resolve(ctx context.Context, slots []KeySlot) ([]byte, error) {
	if c.client == nil {
		return nil, fmt.Errorf("nil KMS client")
	}
	var errs []error
	for _, slot := range slots {
		if slot.SlotType != "kms-platform" {
			continue
		}
		wrapped, err := base64.StdEncoding.DecodeString(slot.WrappedKey)
		if err != nil {
			errs = append(errs, fmt.Errorf("slot %q: decode wrapped key: %w", slot.Label, err))
			continue
		}
		mk, err := c.client.Decrypt(ctx, wrapped)
		if err == nil {
			return mk, nil
		}
		errs = append(errs, fmt.Errorf("slot %q: %w", slot.Label, err))
	}
	return nil, noMatchingSlotErr("kms-platform", errs)
}

func (c kmsClientCred) Wrap(ctx context.Context, masterKey []byte) (KeySlot, error) {
	if c.client == nil {
		return KeySlot{}, fmt.Errorf("nil KMS client")
	}
	ciphertext, err := c.client.Encrypt(ctx, masterKey)
	if err != nil {
		return KeySlot{}, fmt.Errorf("kms encrypt master key: %w", err)
	}
	return KeySlot{
		SlotType:   "kms-platform",
		Label:      DefaultSlotLabel,
		WrappedKey: base64.StdEncoding.EncodeToString(ciphertext),
	}, nil
}

type kmsARNCred struct {
	arn string
}

// WithKMSARN returns a credential using an AWS KMS key ARN, initializing the client on demand.
func WithKMSARN(arn string) Credential {
	return kmsARNCred{arn: arn}
}

func (c kmsARNCred) Resolve(ctx context.Context, slots []KeySlot) ([]byte, error) {
	if c.arn == "" {
		return nil, fmt.Errorf("empty KMS ARN")
	}
	client, err := crypto.NewAWSKMSClient(ctx, c.arn)
	if err != nil {
		return nil, fmt.Errorf("init kms client: %w", err)
	}
	var errs []error
	for _, slot := range slots {
		if slot.SlotType != "kms-platform" {
			continue
		}
		wrapped, err := base64.StdEncoding.DecodeString(slot.WrappedKey)
		if err != nil {
			errs = append(errs, fmt.Errorf("slot %q: decode wrapped key: %w", slot.Label, err))
			continue
		}
		mk, err := client.Decrypt(ctx, wrapped)
		if err == nil {
			return mk, nil
		}
		errs = append(errs, fmt.Errorf("slot %q: %w", slot.Label, err))
	}
	return nil, noMatchingSlotErr("kms-platform", errs)
}

func (c kmsARNCred) Wrap(ctx context.Context, masterKey []byte) (KeySlot, error) {
	if c.arn == "" {
		return KeySlot{}, fmt.Errorf("empty KMS ARN")
	}
	client, err := crypto.NewAWSKMSClient(ctx, c.arn)
	if err != nil {
		return KeySlot{}, fmt.Errorf("init kms client: %w", err)
	}
	ciphertext, err := client.Encrypt(ctx, masterKey)
	if err != nil {
		return KeySlot{}, fmt.Errorf("kms encrypt master key: %w", err)
	}
	return KeySlot{
		SlotType:   "kms-platform",
		Label:      DefaultSlotLabel,
		WrappedKey: base64.StdEncoding.EncodeToString(ciphertext),
	}, nil
}

type promptCred struct {
	resolve func() (string, error)
	wrap    func() (string, error)
}

// WithPrompt returns a credential that prompts for a password interactively.
// The resolve function is used when opening an existing repository (prompts once).
// The wrap function is used when creating new key slots (should prompt with confirmation).
func WithPrompt(resolve, wrap func() (string, error)) Credential {
	return promptCred{resolve: resolve, wrap: wrap}
}

func (c promptCred) Resolve(ctx context.Context, slots []KeySlot) ([]byte, error) {
	if c.resolve == nil {
		return nil, fmt.Errorf("no resolve prompt function")
	}
	// Check if store has password slots first, otherwise don't prompt
	hasPasswordSlot := false
	for _, s := range slots {
		if s.SlotType == "password" {
			hasPasswordSlot = true
			break
		}
	}
	if !hasPasswordSlot {
		return nil, fmt.Errorf("no password key slot found")
	}

	pw, err := c.resolve()
	if err != nil {
		return nil, fmt.Errorf("read password: %w", err)
	}
	return passwordCred{password: pw}.Resolve(ctx, slots)
}

func (c promptCred) Wrap(ctx context.Context, masterKey []byte) (KeySlot, error) {
	if c.wrap == nil {
		return KeySlot{}, fmt.Errorf("no wrap prompt function")
	}
	pw, err := c.wrap()
	if err != nil {
		return KeySlot{}, err
	}
	if pw == "" {
		return KeySlot{}, fmt.Errorf("password cannot be empty")
	}
	return CreatePasswordSlot(masterKey, pw)
}
