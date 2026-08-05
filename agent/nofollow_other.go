//go:build !unix

package agent

// oNoFollow has no portable equivalent outside unix; Windows resolves
// reparse points in the object manager instead. Writes there fall back to
// safePath's resolution alone.
const oNoFollow = 0
