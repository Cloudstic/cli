package keychain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudstic/cli/pkg/store"
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
func LoadKeySlots(ctx context.Context, s store.ObjectStore) ([]KeySlot, error) {
	keys, err := s.List(ctx, store.KeySlotPrefix)
	if err != nil {
		return nil, fmt.Errorf("list key slots: %w", err)
	}
	var slots []KeySlot
	for _, key := range keys {
		data, err := s.Get(ctx, key)
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

func slotObjectKey(slotType, label string) string {
	return store.KeySlotPrefix + slotType + "-" + label
}

func WriteKeySlot(ctx context.Context, s store.ObjectStore, slot KeySlot) error {
	data, err := json.Marshal(slot)
	if err != nil {
		return fmt.Errorf("marshal key slot: %w", err)
	}
	return s.Put(ctx, slotObjectKey(slot.SlotType, slot.Label), data)
}

// HasKeySlots reports whether the store contains any encryption key slots.
func HasKeySlots(ctx context.Context, s store.ObjectStore) bool {
	keys, err := s.List(ctx, store.KeySlotPrefix)
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
