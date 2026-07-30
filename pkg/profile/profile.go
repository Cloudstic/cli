package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/cloudstic/cli/internal/paths"
	"github.com/cloudstic/cli/pkg/secretref"
)

// DefaultFilename is the conventional name of the profiles file inside the
// config directory. See DefaultPath for the full location.
const DefaultFilename = "profiles.yaml"

// Config is the top-level YAML document for backup profiles.
type Config struct {
	Version  int                `yaml:"version"`
	Stores   map[string]Store   `yaml:"stores"`
	Auth     map[string]Auth    `yaml:"auth"`
	Profiles map[string]Profile `yaml:"profiles"`
}

// Store defines reusable backend settings.
type Store struct {
	URI               string `yaml:"uri"`
	S3Endpoint        string `yaml:"s3_endpoint,omitempty"`
	S3Region          string `yaml:"s3_region,omitempty"`
	S3Profile         string `yaml:"s3_profile,omitempty"`
	S3AccessKey       string `yaml:"s3_access_key,omitempty"`
	S3SecretKey       string `yaml:"s3_secret_key,omitempty"`
	B2KeyID           string `yaml:"b2_key_id,omitempty"`
	B2AppKey          string `yaml:"b2_app_key,omitempty"`
	StoreSFTPPassword string `yaml:"store_sftp_password,omitempty"`
	StoreSFTPKey      string `yaml:"store_sftp_key,omitempty"`

	// Encryption: env var indirection for secrets, direct values for non-secrets.
	PasswordSecret          string `yaml:"password_secret,omitempty"`
	EncryptionKeySecret     string `yaml:"encryption_key_secret,omitempty"`
	RecoveryKeySecret       string `yaml:"recovery_key_secret,omitempty"`
	S3AccessKeySecret       string `yaml:"s3_access_key_secret,omitempty"`
	S3SecretKeySecret       string `yaml:"s3_secret_key_secret,omitempty"`
	B2KeyIDSecret           string `yaml:"b2_key_id_secret,omitempty"`
	B2AppKeySecret          string `yaml:"b2_app_key_secret,omitempty"`
	StoreSFTPPasswordSecret string `yaml:"store_sftp_password_secret,omitempty"`
	StoreSFTPKeySecret      string `yaml:"store_sftp_key_secret,omitempty"`
	KMSKeyARN               string `yaml:"kms_key_arn,omitempty"`
	KMSRegion               string `yaml:"kms_region,omitempty"`
	KMSEndpoint             string `yaml:"kms_endpoint,omitempty"`
}

// Profile defines one backup job preset.
type Profile struct {
	Source            string   `yaml:"source"`
	Store             string   `yaml:"store,omitempty"`
	AuthRef           string   `yaml:"auth_ref,omitempty"`
	Tags              []string `yaml:"tags,omitempty"`
	Excludes          []string `yaml:"excludes,omitempty"`
	ExcludeFile       string   `yaml:"exclude_file,omitempty"`
	IgnoreEmpty       bool     `yaml:"ignore_empty,omitempty"`
	SkipNativeFiles   bool     `yaml:"skip_native_files,omitempty"`
	VolumeUUID        string   `yaml:"volume_uuid,omitempty"`
	GoogleCreds       string   `yaml:"google_credentials,omitempty"`
	GoogleCredsRef    string   `yaml:"google_credentials_ref,omitempty"`
	GoogleCredsJSON   string   `yaml:"google_credentials_json,omitempty"`
	GoogleTokenFile   string   `yaml:"google_token_file,omitempty"`
	GoogleTokenRef    string   `yaml:"google_token_ref,omitempty"`
	OneDriveClientID  string   `yaml:"onedrive_client_id,omitempty"`
	OneDriveTokenFile string   `yaml:"onedrive_token_file,omitempty"`
	OneDriveTokenRef  string   `yaml:"onedrive_token_ref,omitempty"`
	Enabled           *bool    `yaml:"enabled,omitempty"`
}

// Auth defines reusable OAuth settings for cloud providers.
type Auth struct {
	Provider          string `yaml:"provider"` // google | onedrive
	GoogleCreds       string `yaml:"google_credentials,omitempty"`
	GoogleCredsRef    string `yaml:"google_credentials_ref,omitempty"`
	GoogleCredsJSON   string `yaml:"google_credentials_json,omitempty"`
	GoogleTokenFile   string `yaml:"google_token_file,omitempty"`
	GoogleTokenRef    string `yaml:"google_token_ref,omitempty"`
	OneDriveClientID  string `yaml:"onedrive_client_id,omitempty"`
	OneDriveTokenFile string `yaml:"onedrive_token_file,omitempty"`
	OneDriveTokenRef  string `yaml:"onedrive_token_ref,omitempty"`
}

// IsEnabled reports whether the profile should be included in -all-profiles.
func (p Profile) IsEnabled() bool {
	if p.Enabled == nil {
		return true
	}
	return *p.Enabled
}

// StoreFor returns the store definition that profile name selects, or nil when
// the profile names no store.
//
// A nil store is not an error. A profile may leave the store to whoever runs
// it — the cloudstic CLI then takes it from -store or CLOUDSTIC_STORE — so
// "this profile says nothing about where the repository is" is a legitimate
// answer, distinct from the broken reference that produces an error.
//
// Resolving the store: pass the result to config.FromProfileStore, which reads
// the secret references it names, or to config.MergeProfileStore to fold it
// under a configuration the caller has already partly decided.
func (c *Config) StoreFor(name string) (*Store, error) {
	p, ok := c.Profiles[name]
	if !ok {
		return nil, fmt.Errorf("unknown profile %q", name)
	}
	if p.Store == "" {
		return nil, nil
	}
	s, ok := c.Stores[p.Store]
	if !ok {
		return nil, fmt.Errorf("profile %q references unknown store %q", name, p.Store)
	}
	return &s, nil
}

// DefaultPath returns where the profiles file lives when no path is given:
// profiles.yaml inside the config directory, which configDir may override (see
// paths.ConfigDir, whose meaning it carries — empty means CLOUDSTIC_CONFIG_DIR
// or the platform default).
//
// It resolves a path without touching the filesystem, so asking where profiles
// would live does not create anything.
//
// CLOUDSTIC_PROFILES_FILE is deliberately not consulted. The cloudstic CLI
// binds it to -profiles-file as an ordinary environment default, so by the time
// a caller needs this, that variable was either applied already or not set.
func DefaultPath(configDir string) (string, error) {
	dir, err := paths.ConfigDir(configDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, DefaultFilename), nil
}

// Normalize returns a config that is safe to read and write into: a nil
// config becomes an empty one, the version defaults to 1, and every map field
// is non-nil.
//
// It is the nil-tolerant form of EnsureMaps, which mutates an existing config
// in place and leaves the version alone. Prefer Normalize when the config may
// be nil or may have come from an empty file.
func Normalize(cfg *Config) *Config { return normalizeConfig(cfg) }

func normalizeConfig(cfg *Config) *Config {
	if cfg == nil {
		cfg = &Config{}
	}
	if cfg.Version == 0 {
		cfg.Version = 1
	}
	if cfg.Stores == nil {
		cfg.Stores = map[string]Store{}
	}
	if cfg.Auth == nil {
		cfg.Auth = map[string]Auth{}
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]Profile{}
	}
	return cfg
}

func validateConfig(cfg *Config) error {
	for storeName, s := range cfg.Stores {
		if err := validateSecretRef("store", storeName, "password_secret", s.PasswordSecret); err != nil {
			return err
		}
		if err := validateSecretRef("store", storeName, "encryption_key_secret", s.EncryptionKeySecret); err != nil {
			return err
		}
		if err := validateSecretRef("store", storeName, "recovery_key_secret", s.RecoveryKeySecret); err != nil {
			return err
		}
		if err := validateSecretRef("store", storeName, "s3_access_key_secret", s.S3AccessKeySecret); err != nil {
			return err
		}
		if err := validateSecretRef("store", storeName, "s3_secret_key_secret", s.S3SecretKeySecret); err != nil {
			return err
		}
		if err := validateSecretRef("store", storeName, "b2_key_id_secret", s.B2KeyIDSecret); err != nil {
			return err
		}
		if err := validateSecretRef("store", storeName, "b2_app_key_secret", s.B2AppKeySecret); err != nil {
			return err
		}
		if err := validateSecretRef("store", storeName, "store_sftp_password_secret", s.StoreSFTPPasswordSecret); err != nil {
			return err
		}
		if err := validateSecretRef("store", storeName, "store_sftp_key_secret", s.StoreSFTPKeySecret); err != nil {
			return err
		}
	}
	for authName, a := range cfg.Auth {
		if err := validateSecretRef("auth", authName, "google_credentials_ref", a.GoogleCredsRef); err != nil {
			return err
		}
		if err := validateSecretRef("auth", authName, "google_token_ref", a.GoogleTokenRef); err != nil {
			return err
		}
		if err := validateSecretRef("auth", authName, "onedrive_token_ref", a.OneDriveTokenRef); err != nil {
			return err
		}
	}
	for profileName, p := range cfg.Profiles {
		if err := validateSecretRef("profile", profileName, "google_credentials_ref", p.GoogleCredsRef); err != nil {
			return err
		}
		if err := validateSecretRef("profile", profileName, "google_token_ref", p.GoogleTokenRef); err != nil {
			return err
		}
		if err := validateSecretRef("profile", profileName, "onedrive_token_ref", p.OneDriveTokenRef); err != nil {
			return err
		}
	}
	return nil
}

func validateSecretRef(entryType, entryName, fieldName, ref string) error {
	if ref == "" {
		return nil
	}
	if _, err := secretref.Parse(ref); err != nil {
		return fmt.Errorf("%s %q field %q: %w", entryType, entryName, fieldName, err)
	}
	return nil
}

// Load reads and parses a profiles YAML file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read profiles file %q: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse profiles file %q: %w", path, err)
	}
	norm := normalizeConfig(&cfg)
	if err := validateConfig(norm); err != nil {
		return nil, fmt.Errorf("validate profiles file %q: %w", path, err)
	}
	return norm, nil
}

// LoadOrEmpty loads profiles from path, treating a missing file
// as an empty, version-1 config rather than an error. Callers that only read
// or manage profiles (list, setup, the TUI) want this; callers running a
// command against a named profile want Load's hard error, since a
// silently-empty config there would misreport "unknown profile" instead of
// "no profiles file".
func LoadOrEmpty(path string) (*Config, error) {
	cfg, err := Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{Version: 1}, nil
		}
		return nil, err
	}
	return cfg, nil
}

// EnsureMaps guarantees cfg's map fields are non-nil, so callers can
// write into them unconditionally.
func EnsureMaps(cfg *Config) {
	if cfg.Stores == nil {
		cfg.Stores = map[string]Store{}
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]Profile{}
	}
	if cfg.Auth == nil {
		cfg.Auth = map[string]Auth{}
	}
}

// Save writes a profiles YAML file atomically.
func Save(path string, cfg *Config) error {
	cfg = normalizeConfig(cfg)
	if err := validateConfig(cfg); err != nil {
		return fmt.Errorf("validate profiles config: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode profiles yaml: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create profiles dir %q: %w", filepath.Dir(path), err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write profiles temp file %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace profiles file %q: %w", path, err)
	}
	return nil
}
