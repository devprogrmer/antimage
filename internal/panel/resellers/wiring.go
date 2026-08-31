package resellers

import "github.com/amyrm/antimage/internal/panel/subjects"

// Compile-time proof that the real subject store satisfies what this package
// needs, so production wiring cannot drift from the test double.
var _ subjectCreator = (*subjects.Store)(nil)
