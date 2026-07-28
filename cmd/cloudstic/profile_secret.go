package main

import secretrefbackends "github.com/cloudstic/cli/pkg/secretref/backends"

// profileSecretResolver is the set of secret-reference schemes this CLI
// understands. pkg/config takes a resolver as a parameter rather than
// defaulting to one, so choosing it is the composition root's job — which for
// this binary means here.
//
// Default() returns a fresh map, so a build that wanted an extra scheme would
// add to it here rather than this being the only set anyone can have.
var profileSecretResolver = secretrefbackends.NewDefaultResolver()
