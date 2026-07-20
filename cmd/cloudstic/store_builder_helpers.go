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

func applyExistingStoreDefaults(g *globalFlags, existing cloudstic.ProfileStore, f storeNewFlagPtrs) {
	if !g.flagProvided("uri") && existing.URI != "" {
		*f.uri = existing.URI
	}
	if !g.flagProvided("s3-region") && existing.S3Region != "" {
		*f.s3Region = existing.S3Region
	}
	if !g.flagProvided("s3-profile") && existing.S3Profile != "" {
		*f.s3Profile = existing.S3Profile
	}
	if !g.flagProvided("s3-endpoint") && existing.S3Endpoint != "" {
		*f.s3Endpoint = existing.S3Endpoint
	}
	if !g.flagProvided("s3-access-key") && existing.S3AccessKey != "" {
		*f.s3AccessKey = existing.S3AccessKey
	}
	if !g.flagProvided("s3-secret-key") && existing.S3SecretKey != "" {
		*f.s3SecretKey = existing.S3SecretKey
	}
	if !g.flagProvided("s3-access-key-secret") && existing.S3AccessKeySecret != "" {
		*f.s3AccessKeySecret = existing.S3AccessKeySecret
	}
	if !g.flagProvided("s3-secret-key-secret") && existing.S3SecretKeySecret != "" {
		*f.s3SecretKeySecret = existing.S3SecretKeySecret
	}
	if !g.flagProvided("store-sftp-password") && existing.StoreSFTPPassword != "" {
		*f.sftpPassword = existing.StoreSFTPPassword
	}
	if !g.flagProvided("store-sftp-key") && existing.StoreSFTPKey != "" {
		*f.sftpKey = existing.StoreSFTPKey
	}
	if !g.flagProvided("store-sftp-password-secret") && existing.StoreSFTPPasswordSecret != "" {
		*f.sftpPasswordSecret = existing.StoreSFTPPasswordSecret
	}
	if !g.flagProvided("store-sftp-key-secret") && existing.StoreSFTPKeySecret != "" {
		*f.sftpKeySecret = existing.StoreSFTPKeySecret
	}
	if !g.flagProvided("password-secret") && existing.PasswordSecret != "" {
		*f.passwordSecret = existing.PasswordSecret
	}
	if !g.flagProvided("encryption-key-secret") && existing.EncryptionKeySecret != "" {
		*f.encryptionKeySecret = existing.EncryptionKeySecret
	}
	if !g.flagProvided("recovery-key-secret") && existing.RecoveryKeySecret != "" {
		*f.recoveryKeySecret = existing.RecoveryKeySecret
	}
	if !g.flagProvided("kms-key-arn") && existing.KMSKeyARN != "" {
		*f.kmsKeyARN = existing.KMSKeyARN
	}
	if !g.flagProvided("kms-region") && existing.KMSRegion != "" {
		*f.kmsRegion = existing.KMSRegion
	}
	if !g.flagProvided("kms-endpoint") && existing.KMSEndpoint != "" {
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
