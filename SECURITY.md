# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability within this project, please report it to us by opening a GitHub security advisory or emailing the maintainers.

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| v1.x    | :white_check_mark: |
| < v1.x  | :x:                |

## Binary Verification

Every `cloudstic` binary released via GitHub is signed using **GitHub Attestations**. This allows you to verify that the binary was built by GitHub Actions from a specific, auditable commit in this repository.

### Identifying a Trusted Binary

To verify a downloaded binary, ensure you have the [GitHub CLI](https://cli.github.com/) installed, then run:

```bash
gh attestation verify ./cloudstic --repo cloudstic/cli
```

The output will confirm if the binary is authentic and provide the specific git commit it was built from.

## Encryption Standards

Cloudstic uses industry-standard encryption to protect your data:

- **AES-256-GCM** for data at rest.
- **BIP39** for recovery key generation.
- **Argon2id** for password-based key derivation.

All encryption is performed locally on your machine. Your master encryption keys never leave your device.
