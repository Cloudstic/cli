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
	sftpPassword        *string
	sftpKey             *string
	sftpPasswordSecret  *string
	sftpKeySecret       *string
	passwordSecret      *string
	encryptionKeySecret *string
	recoveryKeySecret   *string
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
	if !flagsSet["s3-access-key-secret"] && existing.S3AccessKeySecret != "" {
		*f.s3AccessKeySecret = existing.S3AccessKeySecret
	}
	if !flagsSet["s3-secret-key-secret"] && existing.S3SecretKeySecret != "" {
		*f.s3SecretKeySecret = existing.S3SecretKeySecret
	}
	if !flagsSet["store-sftp-password"] && existing.StoreSFTPPassword != "" {
		*f.sftpPassword = existing.StoreSFTPPassword
	}
	if !flagsSet["store-sftp-key"] && existing.StoreSFTPKey != "" {
		*f.sftpKey = existing.StoreSFTPKey
	}
	if !flagsSet["store-sftp-password-secret"] && existing.StoreSFTPPasswordSecret != "" {
		*f.sftpPasswordSecret = existing.StoreSFTPPasswordSecret
	}
	if !flagsSet["store-sftp-key-secret"] && existing.StoreSFTPKeySecret != "" {
		*f.sftpKeySecret = existing.StoreSFTPKeySecret
	}
	if !flagsSet["password-secret"] && existing.PasswordSecret != "" {
		*f.passwordSecret = existing.PasswordSecret
	}
	if !flagsSet["encryption-key-secret"] && existing.EncryptionKeySecret != "" {
		*f.encryptionKeySecret = existing.EncryptionKeySecret
	}
	if !flagsSet["recovery-key-secret"] && existing.RecoveryKeySecret != "" {
		*f.recoveryKeySecret = existing.RecoveryKeySecret
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
		S3AccessKeySecret:       *f.s3AccessKeySecret,
		S3SecretKeySecret:       *f.s3SecretKeySecret,
		StoreSFTPPassword:       *f.sftpPassword,
		StoreSFTPKey:            *f.sftpKey,
		StoreSFTPPasswordSecret: *f.sftpPasswordSecret,
		StoreSFTPKeySecret:      *f.sftpKeySecret,
		PasswordSecret:          *f.passwordSecret,
		EncryptionKeySecret:     *f.encryptionKeySecret,
		RecoveryKeySecret:       *f.recoveryKeySecret,
		KMSKeyARN:               *f.kmsKeyARN,
		KMSRegion:               *f.kmsRegion,
		KMSEndpoint:             *f.kmsEndpoint,
	}
}
