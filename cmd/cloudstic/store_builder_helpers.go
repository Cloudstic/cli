package main

import cloudstic "github.com/cloudstic/cli"

type storeNewFlagPtrs struct {
	uri                 *string
	s3Region            *string
	s3Profile           *string
	s3Endpoint          *string
	s3AccessKey         *string
	s3SecretKey         *string
	s3AccessKeySecret   *string
	s3SecretKeySecret   *string
	s3AccessKeyEnv      *string
	s3SecretKeyEnv      *string
	s3ProfileEnv        *string
	sftpPassword        *string
	sftpKey             *string
	sftpPasswordSecret  *string
	sftpKeySecret       *string
	sftpPasswordEnv     *string
	sftpKeyEnv          *string
	passwordSecret      *string
	encryptionKeySecret *string
	recoveryKeySecret   *string
	passwordEnv         *string
	encryptionKeyEnv    *string
	recoveryKeyEnv      *string
	kmsKeyARN           *string
	kmsRegion           *string
	kmsEndpoint         *string
}

func applyExistingStoreDefaults(flagsSet map[string]bool, existing cloudstic.ProfileStore, f storeNewFlagPtrs) {
	if !flagsSet["uri"] && existing.URI != "" {
		*f.uri = existing.URI
	}
	if !flagsSet["s3-region"] && existing.S3Region != "" {
		*f.s3Region = existing.S3Region
	}
	if !flagsSet["s3-profile"] && existing.S3Profile != "" {
		*f.s3Profile = existing.S3Profile
	}
	if !flagsSet["s3-endpoint"] && existing.S3Endpoint != "" {
		*f.s3Endpoint = existing.S3Endpoint
	}
	if !flagsSet["s3-access-key"] && existing.S3AccessKey != "" {
		*f.s3AccessKey = existing.S3AccessKey
	}
	if !flagsSet["s3-secret-key"] && existing.S3SecretKey != "" {
		*f.s3SecretKey = existing.S3SecretKey
	}
	if !flagsSet["s3-access-key-secret"] && !flagsSet["s3-access-key-env"] {
		*f.s3AccessKeySecret = firstNonEmpty(existing.S3AccessKeySecret, envRef(existing.S3AccessKeyEnv))
	}
	if !flagsSet["s3-secret-key-secret"] && !flagsSet["s3-secret-key-env"] {
		*f.s3SecretKeySecret = firstNonEmpty(existing.S3SecretKeySecret, envRef(existing.S3SecretKeyEnv))
	}
	if !flagsSet["s3-profile-env"] && existing.S3ProfileEnv != "" {
		*f.s3ProfileEnv = existing.S3ProfileEnv
	}
	if !flagsSet["store-sftp-password"] && existing.StoreSFTPPassword != "" {
		*f.sftpPassword = existing.StoreSFTPPassword
	}
	if !flagsSet["store-sftp-key"] && existing.StoreSFTPKey != "" {
		*f.sftpKey = existing.StoreSFTPKey
	}
	if !flagsSet["store-sftp-password-secret"] && !flagsSet["store-sftp-password-env"] {
		*f.sftpPasswordSecret = firstNonEmpty(existing.StoreSFTPPasswordSecret, envRef(existing.StoreSFTPPasswordEnv))
	}
	if !flagsSet["store-sftp-key-secret"] && !flagsSet["store-sftp-key-env"] {
		*f.sftpKeySecret = firstNonEmpty(existing.StoreSFTPKeySecret, envRef(existing.StoreSFTPKeyEnv))
	}
	if !flagsSet["password-secret"] && !flagsSet["password-env"] {
		*f.passwordSecret = firstNonEmpty(existing.PasswordSecret, envRef(existing.PasswordEnv))
	}
	if !flagsSet["encryption-key-secret"] && !flagsSet["encryption-key-env"] {
		*f.encryptionKeySecret = firstNonEmpty(existing.EncryptionKeySecret, envRef(existing.EncryptionKeyEnv))
	}
	if !flagsSet["recovery-key-secret"] && !flagsSet["recovery-key-env"] {
		*f.recoveryKeySecret = firstNonEmpty(existing.RecoveryKeySecret, envRef(existing.RecoveryKeyEnv))
	}
	if !flagsSet["kms-key-arn"] && existing.KMSKeyARN != "" {
		*f.kmsKeyARN = existing.KMSKeyARN
	}
	if !flagsSet["kms-region"] && existing.KMSRegion != "" {
		*f.kmsRegion = existing.KMSRegion
	}
	if !flagsSet["kms-endpoint"] && existing.KMSEndpoint != "" {
		*f.kmsEndpoint = existing.KMSEndpoint
	}
}

func buildProfileStoreFromFlags(f storeNewFlagPtrs) cloudstic.ProfileStore {
	return cloudstic.ProfileStore{
		URI:                     *f.uri,
		S3Region:                *f.s3Region,
		S3Profile:               *f.s3Profile,
		S3Endpoint:              *f.s3Endpoint,
		S3AccessKey:             *f.s3AccessKey,
		S3SecretKey:             *f.s3SecretKey,
		S3AccessKeyEnv:          "",
		S3SecretKeyEnv:          "",
		S3AccessKeySecret:       firstNonEmpty(*f.s3AccessKeySecret, envRef(*f.s3AccessKeyEnv)),
		S3SecretKeySecret:       firstNonEmpty(*f.s3SecretKeySecret, envRef(*f.s3SecretKeyEnv)),
		S3ProfileEnv:            *f.s3ProfileEnv,
		StoreSFTPPassword:       *f.sftpPassword,
		StoreSFTPKey:            *f.sftpKey,
		StoreSFTPPasswordEnv:    "",
		StoreSFTPKeyEnv:         "",
		StoreSFTPPasswordSecret: firstNonEmpty(*f.sftpPasswordSecret, envRef(*f.sftpPasswordEnv)),
		StoreSFTPKeySecret:      firstNonEmpty(*f.sftpKeySecret, envRef(*f.sftpKeyEnv)),
		PasswordEnv:             "",
		EncryptionKeyEnv:        "",
		RecoveryKeyEnv:          "",
		PasswordSecret:          firstNonEmpty(*f.passwordSecret, envRef(*f.passwordEnv)),
		EncryptionKeySecret:     firstNonEmpty(*f.encryptionKeySecret, envRef(*f.encryptionKeyEnv)),
		RecoveryKeySecret:       firstNonEmpty(*f.recoveryKeySecret, envRef(*f.recoveryKeyEnv)),
		KMSKeyARN:               *f.kmsKeyARN,
		KMSRegion:               *f.kmsRegion,
		KMSEndpoint:             *f.kmsEndpoint,
	}
}
