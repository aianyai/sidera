package crashwalk

// NotYetWalked lists the declarations whose write points no acceptance recipe has
// walked yet, each with the reason it is still safe to leave. Assess reports their
// declared keys as Coverage.Deferred and not as missing, so the walk of the built
// binary and the catalog evidence of the in-process layer, which both fail on a
// declared write that is never reached, see one answer. A listed declaration that a
// walk does reach is reported as stale in Coverage.StaleDeferred, so the list can
// only shrink as recipes are written.
//
// Delete an entry when the recipe that walks its declaration is written.
var NotYetWalked = map[string]string{
	"trust-credential":         "not yet walked by the acceptance recipes; the scenario rows of the store cover it",
	"trust-revocation":         "not yet walked by the acceptance recipes; the scenario rows of the store cover it",
	"trust-local-clear":        "not yet walked by the acceptance recipes; the scenario rows of the store cover it",
	"peer-cert":                "not yet walked by the acceptance recipes; the scenario rows of the store cover it",
	"pairing-target-put":       "not yet walked by the acceptance recipes; the scenario rows of the store cover it",
	"pairing-target-delete":    "not yet walked by the acceptance recipes; the scenario rows of the store cover it",
	"pairing-initiator-put":    "not yet walked by the acceptance recipes; the scenario rows of the store cover it",
	"pairing-initiator-delete": "not yet walked by the acceptance recipes; the scenario rows of the store cover it",
	"pairing-limits":           "not yet walked by the acceptance recipes; the scenario rows of the store cover it",
	"pairing-expectations":     "not yet walked by the acceptance recipes; the scenario rows of the store cover it",
	"trust-local-revoke":       "not yet walked by the acceptance recipes; the crash walk of the engine covers it",
	"trust-expect":             "not yet walked by the acceptance recipes; the crash walk of the engine covers it",
	"trust-forget":             "not yet walked by the acceptance recipes; the crash walk of the engine covers it",
	"trust-request":            "not yet walked by the acceptance recipes; the crash walk of the engine covers it",
	"trust-reject":             "not yet walked by the acceptance recipes; the crash walk of the engine covers it",
	"trust-clear":              "not yet walked by the acceptance recipes; the crash walk of the engine covers it",
	"trust-result":             "not yet walked by the acceptance recipes; the crash walk of the engine covers it",
	"network-set":              "the network configuration write is not yet walked by the acceptance recipes",
}
