// A stand-in for a third-party module implementing Cloudstic's public
// contracts. It lives under testdata/ so the go tool ignores it, and is built
// by TestExternalModuleImplementsPublicContracts.
module example.com/externalmod

go 1.26.0

require github.com/cloudstic/cli v0.0.0

replace github.com/cloudstic/cli => ../../../..
