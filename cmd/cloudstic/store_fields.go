package main

// The hand-written store-field table: the 22 fields `store new` accepts, the
// rule for prefilling them from an existing entry, and the assembly of a
// profile.Store from them.
//
// This is one of six places the same field set is written out — here twice,
// plus declareStoreNewArgs, secretDisplayRows, the TUI store form, and
// profile.Store itself. pkg/config/field.go already drives the *read*
// direction from a single table (fieldSpecs); the write direction has no
// equivalent, which is why this exists and why nothing checks that it stays
// complete. Folding it into that table is the intended fix.

import "github.com/cloudstic/cli/pkg/profile"

type storeNewFlagPtrs struct {
	uri                 *string
	s3Region            *string
	s3Profile           *string
	s3Endpoint          *string
	s3AccessKey         *string
	s3SecretKey         *string
	s3AccessKeySecret   *string
	s3SecretKeySecret   *string
	b2KeyID             *string
	b2AppKey            *string
	b2KeyIDSecret       *string
	b2AppKeySecret      *string
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

func applyExistingStoreDefaults(g *globalFlags, existing profile.Store, f storeNewFlagPtrs) {
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
	if !g.flagProvided("b2-key-id") && existing.B2KeyID != "" {
		*f.b2KeyID = existing.B2KeyID
	}
	if !g.flagProvided("b2-app-key") && existing.B2AppKey != "" {
		*f.b2AppKey = existing.B2AppKey
	}
	if !g.flagProvided("b2-key-id-secret") && existing.B2KeyIDSecret != "" {
		*f.b2KeyIDSecret = existing.B2KeyIDSecret
	}
	if !g.flagProvided("b2-app-key-secret") && existing.B2AppKeySecret != "" {
		*f.b2AppKeySecret = existing.B2AppKeySecret
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

func buildProfileStoreFromFlags(f storeNewFlagPtrs) profile.Store {
	return profile.Store{
		URI:                     *f.uri,
		S3Region:                *f.s3Region,
		S3Profile:               *f.s3Profile,
		S3Endpoint:              *f.s3Endpoint,
		S3AccessKey:             *f.s3AccessKey,
		S3SecretKey:             *f.s3SecretKey,
		S3AccessKeySecret:       *f.s3AccessKeySecret,
		S3SecretKeySecret:       *f.s3SecretKeySecret,
		B2KeyID:                 *f.b2KeyID,
		B2AppKey:                *f.b2AppKey,
		B2KeyIDSecret:           *f.b2KeyIDSecret,
		B2AppKeySecret:          *f.b2AppKeySecret,
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
