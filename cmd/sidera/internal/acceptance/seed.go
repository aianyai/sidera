package acceptance

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// processSeed separates node processes by their role and observed generation.
// The fixture root and process identity never enter replayable random streams.
func (f *Fixture) processSeed(role string, generation int) string {
	return fmt.Sprintf("%s/%s/%d", f.opts.Seed, role, generation)
}

// A native session survives daemon generations. Its seed belongs to the scenario
// and adapter instance instead, so a replacement adapter finds the same session.
func (f *Fixture) runtimeSeed(instance string) uint64 {
	sum := sha256.Sum256([]byte(f.opts.Seed + "/runtime/" + instance))
	return binary.BigEndian.Uint64(sum[:8])
}
