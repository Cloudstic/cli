package engine

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ProfilesConfig is the top-level YAML document for backup profiles.
type ProfilesConfig struct {
	Version  int                      `yaml:"version"`
	Stores   map[string]ProfileStore  `yaml:"stores"`
	Auth     map[string]ProfileAuth   `yaml:"auth"`
	Profiles map[string]BackupProfile `yaml:"profiles"`
}

// ProfileStore defines reusable backend settings.
type ProfileStore struct {
	URI                  string `yaml:"uri"`
	S3Endpoint           string `yaml:"s3_endpoint,omitempty"`
	S3Region             string `yaml:"s3_region,omitempty"`
	S3Profile            string `yaml:"s3_profile,omitempty"`
	S3AccessKey          string `yaml:"s3_access_key,omitempty"`
	S3SecretKey          string `yaml:"s3_secret_key,omitempty"`
	S3AccessKeyEnv       string `yaml:"s3_access_key_env,omitempty"`
	S3SecretKeyEnv       string `yaml:"s3_secret_key_env,omitempty"`
	S3ProfileEnv         string `yaml:"s3_profile_env,omitempty"`
	StoreSFTPPassword    string `yaml:"store_sftp_password,omitempty"`
	StoreSFTPKey         string `yaml:"store_sftp_key,omitempty"`
	StoreSFTPPasswordEnv string `yaml:"store_sftp_password_env,omitempty"`
	StoreSFTPKeyEnv      string `yaml:"store_sftp_key_env,omitempty"`

	// Encryption: env var indirection for secrets, direct values for non-secrets.
	PasswordEnv      string `yaml:"password_env,omitempty"`
	EncryptionKeyEnv string `yaml:"encryption_key_env,omitempty"`
	RecoveryKeyEnv   string `yaml:"recovery_key_env,omitempty"`
	KMSKeyARN        string `yaml:"kms_key_arn,omitempty"`
	KMSRegion        string `yaml:"kms_region,omitempty"`
	KMSEndpoint      string `yaml:"kms_endpoint,omitempty"`
}

// BackupProfile defines one backup job preset.
type BackupProfile struct {
	Source            string   `yaml:"source"`
	Store             string   `yaml:"store,omitempty"`
	AuthRef           string   `yaml:"auth_ref,omitempty"`
	Tags              []string `yaml:"tags,omitempty"`
	Excludes          []string `yaml:"excludes,omitempty"`
	ExcludeFile       string   `yaml:"exclude_file,omitempty"`
	SkipNativeFiles   bool     `yaml:"skip_native_files,omitempty"`
	VolumeUUID        string   `yaml:"volume_uuid,omitempty"`
	GoogleCreds       string   `yaml:"google_credentials,omitempty"`
	GoogleTokenFile   string   `yaml:"google_token_file,omitempty"`
	OneDriveClientID  string   `yaml:"onedrive_client_id,omitempty"`
	OneDriveTokenFile string   `yaml:"onedrive_token_file,omitempty"`
	Enabled           *bool    `yaml:"enabled,omitempty"`
}

// ProfileAuth defines reusable OAuth settings for cloud providers.
type ProfileAuth struct {
	Provider          string `yaml:"provider"` // google | onedrive
	GoogleCreds       string `yaml:"google_credentials,omitempty"`
	GoogleTokenFile   string `yaml:"google_token_file,omitempty"`
	OneDriveClientID  string `yaml:"onedrive_client_id,omitempty"`
	OneDriveTokenFile string `yaml:"onedrive_token_file,omitempty"`
}

// IsEnabled reports whether the profile should be included in -all-profiles.
func (p BackupProfile) IsEnabled() bool {
	if p.Enabled == nil {
		return true
	}
	return *p.Enabled
}

func normalizeProfilesConfig(cfg *ProfilesConfig) *ProfilesConfig {
	if cfg == nil {
		cfg = &ProfilesConfig{}
	}
	if cfg.Version == 0 {
		cfg.Version = 1
	}
	if cfg.Stores == nil {
		cfg.Stores = map[string]ProfileStore{}
	}
	if cfg.Auth == nil {
		cfg.Auth = map[string]ProfileAuth{}
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]BackupProfile{}
	}
	return cfg
}

// LoadProfilesFile reads and parses a profiles YAML file.
func LoadProfilesFile(path string) (*ProfilesConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read profiles file %q: %w", path, err)
	}
	var cfg ProfilesConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse profiles file %q: %w", path, err)
	}
	return normalizeProfilesConfig(&cfg), nil
}

// SaveProfilesFile writes a profiles YAML file atomically.
func SaveProfilesFile(path string, cfg *ProfilesConfig) error {
	cfg = normalizeProfilesConfig(cfg)

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
