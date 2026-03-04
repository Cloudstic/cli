# Encryption Design

## Overview

All backup data (chunks, metadata, snapshots) is encrypted at rest using
AES-256-GCM. Encryption is transparent at the object store layer — the backup
engine does not need to be aware of it.

## Threat Model

- **At-rest protection**: if B2 or PostgreSQL storage is compromised, data is
  unreadable without the encryption key.
- **Tenant isolation**: even if database RLS is bypassed, each tenant's data is
  encrypted with a unique key.
- **Key loss prevention (SaaS)**: the platform always holds a recovery path via
  the platform key slot.

## Ciphertext Format

Every encrypted value follows the same binary layout:

```
version (1 byte) || nonce (12 bytes) || ciphertext || GCM tag (16 bytes)
```

- **Version `0x01`**: AES-256-GCM, 12-byte random nonce
- Overhead: 29 bytes per object (negligible for chunks at 512 KB–8 MB,
  small for metadata objects)

On read, if the first byte is not a recognised version, the data is returned
as-is (plaintext). This allows gradual migration from unencrypted to encrypted
storage. Existing unencrypted data is safe because it starts with either gzip
magic bytes (`0x1f 0x8b`) or JSON (`0x7b`), neither of which collides with
valid version bytes.

## Key Hierarchy

```
Platform Key (env var / KMS)
  └─ wraps → Platform Slot ─── unwraps → Tenant Master Key (256-bit)
                                              │
User Password (optional)                      │
  └─ Argon2id derive + wrap → Password Slot ──┘
                                              │
Recovery Key (optional, BIP39 mnemonic)       │
  └─ wraps → Recovery Slot ───────────────────┘
                                              │
                                     HKDF-SHA256(master, info="cloudstic-backup-v1")
                                              │
                                       Encryption Key (256-bit AES)
                                              ├──────────── EncryptedStore
                                              │
                                     HKDF-SHA256(enc_key, info="cloudstic-dedup-mac-v1")
                                              │
                                        Dedup HMAC Key (256-bit)
                                              │
                                       Chunker (HMAC-SHA256 refs)
```

### Master key

Each tenant has a 256-bit random master key generated from `crypto/rand` at
tenant creation. The master key is never stored in plaintext.

### Key slots

A key slot stores the master key encrypted ("wrapped") by a wrapping key.
Multiple slots can coexist for the same tenant, each using a different wrapping
key.

| Slot type      | Wrapping key source                  | Purpose                                  |
|----------------|--------------------------------------|------------------------------------------|
| `platform`     | `PLATFORM_ENCRYPTION_KEY` env var    | Legacy platform recovery (plaintext key) |
| `kms-platform` | AWS KMS CMK (envelope encryption)    | HSM-backed platform recovery             |
| `password`     | Argon2id(user password)              | Zero-knowledge; user controls access     |
| `recovery`     | Random 256-bit key (BIP39 mnemonic)  | Offline backup; printed / stored safely  |

### Key slot storage

Key slots are stored in two locations:

1. **PostgreSQL** (`app.encryption_key_slots`): primary source for the web
   application, fast access during backup/restore setup.
2. **B2** (`keys/<slot_type>-<label>` objects): best-effort copy written at
   tenant creation. Enables the CLI to discover and use encryption keys
   directly from the repository without database access, and serves as
   disaster recovery if PostgreSQL is lost.

The B2 key slot objects are JSON:

```json
{
  "slot_type": "platform",
  "wrapped_key": "base64(nonce || encrypted_master_key || tag)",
  "label": "default"
}
```

Key slots are **not encrypted** by `EncryptedStore` — they are stored as
plaintext JSON (containing already-wrapped keys). The `EncryptedStore`
passes through any object under the `keys/` prefix without encrypting or
decrypting it, avoiding the chicken-and-egg problem of needing the
encryption key to read the encryption key.

### Key derivation

The master key is not used directly for encryption. Instead, HKDF-SHA256
derives a 256-bit AES key:

```
encryption_key = HKDF-SHA256(
    secret = master_key,
    salt   = "",
    info   = "cloudstic-backup-v1",
)
```

A second key for chunk deduplication (HMAC) is derived from the encryption
key:

```
dedup_hmac_key = HKDF-SHA256(
    secret = encryption_key,
    salt   = "",
    info   = "cloudstic-dedup-mac-v1",
)
```

This keeps the public API surface unchanged (a single encryption key is
passed around) while the HMAC key is derived internally at point of use.
HKDF is a PRF, so chaining derivations is cryptographically sound — the
dedup key is independent from the encryption key. If only the dedup key
leaks, the encryption key remains safe (HKDF is one-way).

## Store Stack

Encryption sits in the object store wrapper chain:

```
Backup Engine → CompressedStore → EncryptedStore → MeteredStore → PackStore → Backend
                                                                                └─ S3 / B2 / Local / SFTP
```

- **Put(key, data)**: encrypt `data`, delegate `Put(key, encrypted)` to inner
  store. Objects under `keys/` are passed through unencrypted.
- **Get(key)**: delegate to inner store, decrypt result (or return as-is if
  unencrypted legacy data). Objects under `keys/` are returned as-is.
- **Exists, List, Delete, Size, TotalSize**: pass through unchanged

Content addressing is preserved: chunk keys are `chunk/<hmac_sha256>` where
the hash is an HMAC-SHA256 keyed by the dedup key. This prevents the storage
provider from confirming file existence by hashing known plaintext
("confirmation-of-a-file" attack). Without the dedup key, the provider
cannot reproduce chunk references.

## Content Addressing and Dedup

Encryption uses random nonces, so encrypting the same plaintext twice produces
different ciphertext. Dedup still works because:

1. **Chunk keys** are HMAC-SHA256 hashes of plaintext keyed by the dedup key.
   All other object keys (`content/`, `filemeta/`, `node/`, `snapshot/`) use
   plain SHA-256
2. Before writing, the engine checks `Exists(key)` — if the key exists, the
   write is skipped entirely
3. Within a tenant, identical files produce identical HMAC chunk hashes and
   dedup normally
4. Different tenants (different keys) produce different chunk hashes, so there
   is no cross-tenant dedup — this is by design

## Key Rotation

### Platform key rotation

When `PLATFORM_ENCRYPTION_KEY` changes (e.g., env var rotation):

1. Unwrap every tenant master key with the **old** platform key
2. Re-wrap each master key with the **new** platform key
3. Update the `wrapped_key` column in `encryption_key_slots`

This is cheap — no backup data re-encryption. It touches one row per tenant
and runs in seconds even at scale.

### Tenant master key rotation (rare)

When a tenant's master key must change (security incident):

1. Generate new master key
2. Create new key slots with the new master key
3. Keep old key in memory for dual-key reads
4. New writes use new key; reads try new key first, fall back to old key on
   GCM authentication failure
5. Background job re-encrypts all existing objects
6. Once complete, retire old key slots

This is expensive (reads + re-encrypts every object) and should only be needed
for security incidents.

## Database Schema

```sql
CREATE TABLE app.encryption_key_slots (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL REFERENCES app.tenants(id) ON DELETE CASCADE,
    slot_type     TEXT NOT NULL CHECK (slot_type IN ('platform', 'kms-platform', 'password', 'recovery')),
    wrapped_key   TEXT NOT NULL,  -- base64(nonce || encrypted_master_key || tag)
    kdf_params    JSONB,          -- for password slots: {algorithm, salt, n, r, p}
    label         TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, slot_type, label)
);
```

This table uses RLS for tenant isolation. Key slots are also written to B2
as `keys/<slot_type>-<label>` objects (best-effort) to enable CLI access
and disaster recovery.

## Recovery Key

The recovery key is a 256-bit random key encoded as a **BIP39 24-word
mnemonic** (seed phrase). It provides an offline backup mechanism: if the
user loses their password or the platform key is unavailable, the mnemonic
can unlock the master key.

### How it works

1. A 256-bit random key is generated from `crypto/rand`
2. The key is encoded as a 24-word BIP39 English mnemonic
3. The master key is wrapped (AES-256-GCM) using the raw recovery key
4. The wrapped key is stored as a `recovery` slot (`keys/recovery-default`)
5. The mnemonic is displayed **once** to the user — it is never stored

To recover:

1. The user provides the 24-word mnemonic
2. The mnemonic is decoded back to the 256-bit raw key
3. The raw key unwraps the master key from the recovery slot
4. HKDF derives the encryption key — same path as platform/password slots

### CLI usage

Generate a recovery key during repository initialization:

```
cloudstic init --encryption-password <pw> --recovery
```

Or add a recovery key to an existing repository:

```
cloudstic add-recovery-key --encryption-password <pw>
```

Open a repository using the recovery key:

```
cloudstic backup --recovery-key "word1 word2 ... word24"
```

The recovery key can also be provided via the `CLOUDSTIC_RECOVERY_KEY`
environment variable.

### Web (SaaS) usage

The `EncryptionService.CreateRecoverySlot` method generates a recovery key
for a tenant, stores the slot in PostgreSQL and B2, and returns the mnemonic
for one-time display. `HasRecoverySlot` checks whether a recovery slot
already exists.

## CLI Encryption

The `EncryptedStore` and crypto primitives live in `cli/pkg/` and are shared
by both the CLI tool and the web application. Only key management differs:

| Aspect           | Web (SaaS)                             | CLI                                  |
|------------------|----------------------------------------|--------------------------------------|
| Key management   | Platform-managed, stored in DB + B2    | User-managed password or platform key|
| Key derivation   | Platform key wraps master key          | Argon2id(password) wraps master key  |
| Key storage      | `encryption_key_slots` table + B2      | `keys/<type>-<label>` in B2          |
| User experience  | Transparent, no password needed        | Credential per operation             |
| Key loss risk    | None (platform always has recovery)    | Recovery key mitigates password loss |

Both web and CLI store key slots as `keys/<slot_type>-<label>` objects in B2,
making repositories self-contained. The ciphertext format is identical, so
repositories are interoperable if you have the key.

### CLI flow

1. List `keys/*` objects from B2 to discover available slots
2. If `-kms-key-arn` is provided, try `kms-platform` slots first (AWS KMS decryption)
3. Try platform key, password, or recovery key based on provided credentials
4. If no credential matched and stdin is a terminal, prompt the user for the repository password interactively
5. Unwrap the master key, derive the encryption key via HKDF
6. Create `EncryptedStore` with that key — same code path as the web

## Platform Key Management

### KMS-backed keys (recommended)

The preferred approach uses AWS KMS Customer Managed Keys (CMKs) for
envelope encryption. The master key is wrapped by KMS (`kms-platform`
slots), so the plaintext wrapping key never leaves the HSM.

- The web server uses `PLATFORM_KMS_KEY_ARN` and `TOKEN_KMS_KEY_ARN`
  environment variables pointing to KMS key ARNs
- The CLI uses `-kms-key-arn` flag or `CLOUDSTIC_KMS_KEY_ARN` env var
- KMS keys are configured with automatic annual rotation
- IAM policies restrict access to Encrypt/Decrypt/GenerateDataKey/DescribeKey
- No plaintext key material is stored in environment variables or secrets

### Legacy plaintext keys

The `PLATFORM_ENCRYPTION_KEY` environment variable holds a 32-byte
hex-encoded key. This is supported for backward compatibility.

- Store in a secrets manager (Vault, AWS Secrets Manager, etc.)
- Back up securely — losing this key means generating new master keys for all
  tenants (existing encrypted data becomes unreadable)
- Rotate with the platform key rotation flow described above

### Migration

Both key types can coexist. When KMS is configured, new tenants get
`kms-platform` slots. Existing `platform` slots remain readable with the
legacy key. The system tries KMS slots first, then falls back to legacy.
