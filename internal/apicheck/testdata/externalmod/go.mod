// A stand-in for a third-party module implementing Cloudstic's public
// contracts. It lives under testdata/ so the go tool ignores it, and is built
// by TestExternalModuleImplementsPublicContracts.
module example.com/externalmod

go 1.26.0

require github.com/cloudstic/cli v0.0.0

require (
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/keybase/go-keychain v0.0.1 // indirect
	github.com/tyler-smith/go-bip39 v1.1.0 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/cloudstic/cli => ../../../..
