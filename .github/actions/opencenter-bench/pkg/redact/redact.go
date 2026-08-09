// Package redact re-exports the bench's redactor so other modules can use it.
//
// Go will not let one module import another's internal/ packages, and the E2E
// lifecycle bench needs exactly this one: it writes command output, generated
// files and diagnostics to disk, and every one of those paths must be redacted
// by the same rules the console already uses.
//
// Aliases, not a copy. `type Redactor = redact.Redactor` is the same type, so a
// value made here is accepted anywhere the internal package is used and there is
// no second implementation to drift. Copying 460 lines of secret-matching into
// a second repository is how two redactors end up disagreeing about what a
// secret looks like, and the one that is wrong is the one nobody is reading.
package redact

import "github.com/opencenter-cloud/opencli-testbench/internal/redact"

// Redactor removes secrets from anything on its way out.
type Redactor = redact.Redactor

// New returns an empty redactor.
func New() *Redactor { return redact.New() }

// IsSecretName reports whether a variable name names a secret.
func IsSecretName(name string) bool { return redact.IsSecretName(name) }
