package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/cloudstic/cli/pkg/crypto"

	"github.com/jackc/pgx/v5"
)

// KeySlot is the JSON representation of an encryption key slot stored in B2.
type KeySlot struct {
	SlotType   string     `json:"slot_type"`
	WrappedKey string     `json:"wrapped_key"`
	Label      string     `json:"label"`
	KDFParams  *KDFParams `json:"kdf_params,omitempty"`
}

// KDFParams holds the parameters for password-based key derivation.
type KDFParams struct {
	Algorithm string `json:"algorithm"`
	Salt      string `json:"salt"` // base64-encoded
	Time      uint32 `json:"time"`
	Memory    uint32 `json:"memory"`
	Threads   uint8  `json:"threads"`
}

// LoadKeySlots reads all key slot objects from the store.
func LoadKeySlots(s ObjectStore) ([]KeySlot, error) {
	keys, err := s.List(KeySlotPrefix)
	if err != nil {
		return nil, fmt.Errorf("list key slots: %w", err)
	}
	var slots []KeySlot
	for _, key := range keys {
		data, err := s.Get(key)
		if err != nil {
			return nil, fmt.Errorf("read key slot %s: %w", key, err)
		}
		var slot KeySlot
		if err := json.Unmarshal(data, &slot); err != nil {
			return nil, fmt.Errorf("parse key slot %s: %w", key, err)
		}
		slots = append(slots, slot)
	}
	return slots, nil
}

// LoadKeySlotsFromDB reads key slots directly from the encryption_key_slots
// PostgreSQL table via a TxFunc. This is needed for HybridStore because the
// web writes key slots to this table (not to app.objects).
func LoadKeySlotsFromDB(db TxFunc) ([]KeySlot, error) {
	var slots []KeySlot
	err := db(func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT slot_type, wrapped_key, COALESCE(label, '') FROM app.encryption_key_slots
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s KeySlot
			if err := rows.Scan(&s.SlotType, &s.WrappedKey, &s.Label); err != nil {
				return err
			}
			slots = append(slots, s)
		}
		return rows.Err()
	})
	return slots, err
}

func slotObjectKey(slotType, label string) string {
	return KeySlotPrefix + slotType + "-" + label
}

func writeSlot(s ObjectStore, slot KeySlot) error {
	data, err := json.Marshal(slot)
	if err != nil {
		return fmt.Errorf("marshal key slot: %w", err)
	}
	return s.Put(slotObjectKey(slot.SlotType, slot.Label), data)
}

// unwrapMasterKey tries to unwrap the master key from a slot using the given
// wrapping key. Returns the master key on success.
func unwrapMasterKey(slot KeySlot, wrappingKey []byte) ([]byte, error) {
	wrapped, err := base64.StdEncoding.DecodeString(slot.WrappedKey)
	if err != nil {
		return nil, fmt.Errorf("decode wrapped key: %w", err)
	}
	return crypto.UnwrapKey(wrapped, wrappingKey)
}

// deriveEncryptionKey derives the AES-256 encryption key from a master key.
func deriveEncryptionKey(masterKey []byte) ([]byte, error) {
	return crypto.DeriveKey(masterKey, crypto.HKDFInfoBackupV1)
}

// OpenWithPlatformKey finds a platform slot, unwraps the master key using the
// given platform key, and returns the derived encryption key.
func OpenWithPlatformKey(slots []KeySlot, platformKey []byte) ([]byte, error) {
	for _, slot := range slots {
		if slot.SlotType != "platform" {
			continue
		}
		masterKey, err := unwrapMasterKey(slot, platformKey)
		if err != nil {
			continue
		}
		return deriveEncryptionKey(masterKey)
	}
	return nil, fmt.Errorf("no compatible platform key slot found (wrong key?)")
}

// OpenWithPassword finds a password slot, derives the wrapping key using
// Argon2id, unwraps the master key, and returns the derived encryption key.
func OpenWithPassword(slots []KeySlot, password string) ([]byte, error) {
	for _, slot := range slots {
		if slot.SlotType != "password" || slot.KDFParams == nil {
			continue
		}
		salt, err := base64.StdEncoding.DecodeString(slot.KDFParams.Salt)
		if err != nil {
			continue
		}
		wrappingKey := crypto.DeriveKeyFromPassword(password, salt, crypto.Argon2Params{
			Time:    slot.KDFParams.Time,
			Memory:  slot.KDFParams.Memory,
			Threads: slot.KDFParams.Threads,
		})
		masterKey, err := unwrapMasterKey(slot, wrappingKey)
		if err != nil {
			continue
		}
		return deriveEncryptionKey(masterKey)
	}
	return nil, fmt.Errorf("no compatible password key slot found (wrong password?)")
}

// InitEncryptionKey initializes encryption for a new repository. It generates
// a master key and creates key slots for whatever credentials are provided.
// At least one of platformKey or password must be non-empty.
// Returns the derived encryption key.
func InitEncryptionKey(s ObjectStore, platformKey []byte, password string) ([]byte, error) {
	masterKey, err := crypto.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	if len(platformKey) > 0 {
		if err := writePlatformSlot(s, masterKey, platformKey); err != nil {
			return nil, err
		}
	}
	if password != "" {
		if err := writePasswordSlot(s, masterKey, password); err != nil {
			return nil, err
		}
	}
	return deriveEncryptionKey(masterKey)
}

func writePlatformSlot(s ObjectStore, masterKey, platformKey []byte) error {
	wrapped, err := crypto.WrapKey(masterKey, platformKey)
	if err != nil {
		return fmt.Errorf("wrap master key with platform key: %w", err)
	}
	return writeSlot(s, KeySlot{
		SlotType:   "platform",
		WrappedKey: base64.StdEncoding.EncodeToString(wrapped),
		Label:      "default",
	})
}

func writePasswordSlot(s ObjectStore, masterKey []byte, password string) error {
	salt := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return fmt.Errorf("generate salt: %w", err)
	}
	params := crypto.DefaultArgon2Params
	wrappingKey := crypto.DeriveKeyFromPassword(password, salt, params)
	wrapped, err := crypto.WrapKey(masterKey, wrappingKey)
	if err != nil {
		return fmt.Errorf("wrap master key with password: %w", err)
	}
	return writeSlot(s, KeySlot{
		SlotType:   "password",
		WrappedKey: base64.StdEncoding.EncodeToString(wrapped),
		Label:      "default",
		KDFParams: &KDFParams{
			Algorithm: "argon2id",
			Salt:      base64.StdEncoding.EncodeToString(salt),
			Time:      params.Time,
			Memory:    params.Memory,
			Threads:   params.Threads,
		},
	})
}

// OpenWithRecoveryKey finds a recovery slot, unwraps the master key using the
// given raw recovery key, and returns the derived encryption key.
func OpenWithRecoveryKey(slots []KeySlot, recoveryKey []byte) ([]byte, error) {
	for _, slot := range slots {
		if slot.SlotType != "recovery" {
			continue
		}
		masterKey, err := unwrapMasterKey(slot, recoveryKey)
		if err != nil {
			continue
		}
		return deriveEncryptionKey(masterKey)
	}
	return nil, fmt.Errorf("no compatible recovery key slot found (wrong key?)")
}

// ExtractMasterKey unwraps and returns the raw master key from whichever
// credential matches. Unlike the OpenWith* functions that return a derived
// encryption key, this returns the master key itself — needed when adding
// new key slots to an existing repo.
func ExtractMasterKey(slots []KeySlot, platformKey []byte, password string) ([]byte, error) {
	for _, slot := range slots {
		if slot.SlotType == "platform" && len(platformKey) > 0 {
			if mk, err := unwrapMasterKey(slot, platformKey); err == nil {
				return mk, nil
			}
		}
		if slot.SlotType == "password" && password != "" && slot.KDFParams != nil {
			salt, err := base64.StdEncoding.DecodeString(slot.KDFParams.Salt)
			if err != nil {
				continue
			}
			wk := crypto.DeriveKeyFromPassword(password, salt, crypto.Argon2Params{
				Time:    slot.KDFParams.Time,
				Memory:  slot.KDFParams.Memory,
				Threads: slot.KDFParams.Threads,
			})
			if mk, err := unwrapMasterKey(slot, wk); err == nil {
				return mk, nil
			}
		}
	}
	return nil, fmt.Errorf("could not extract master key: no provided credential matches")
}

// AddRecoverySlot generates a recovery key, wraps the given master key with
// it, stores the recovery slot, and returns the BIP39 24-word mnemonic.
func AddRecoverySlot(s ObjectStore, masterKey []byte) (mnemonic string, err error) {
	mnemonic, recoveryKey, err := crypto.GenerateRecoveryMnemonic()
	if err != nil {
		return "", err
	}
	if err := writeRecoverySlot(s, masterKey, recoveryKey); err != nil {
		return "", err
	}
	return mnemonic, nil
}

func writeRecoverySlot(s ObjectStore, masterKey, recoveryKey []byte) error {
	wrapped, err := crypto.WrapKey(masterKey, recoveryKey)
	if err != nil {
		return fmt.Errorf("wrap master key with recovery key: %w", err)
	}
	return writeSlot(s, KeySlot{
		SlotType:   "recovery",
		WrappedKey: base64.StdEncoding.EncodeToString(wrapped),
		Label:      "default",
	})
}

// SyncKeySlots writes the given key slots to the store (best-effort).
// This is used to keep B2 in sync with the authoritative DB slots so that
// B2-only mode can find the correct key.
func SyncKeySlots(s ObjectStore, slots []KeySlot) {
	for _, slot := range slots {
		_ = writeSlot(s, slot)
	}
}

// HasKeySlots reports whether the store contains any encryption key slots.
func HasKeySlots(s ObjectStore) bool {
	keys, err := s.List(KeySlotPrefix)
	return err == nil && len(keys) > 0
}

// SlotTypes returns the slot types present among the given slots.
func SlotTypes(slots []KeySlot) string {
	types := make(map[string]bool)
	for _, s := range slots {
		types[s.SlotType] = true
	}
	var out []string
	for t := range types {
		out = append(out, t)
	}
	return strings.Join(out, ", ")
}
