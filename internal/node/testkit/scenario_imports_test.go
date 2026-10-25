package testkit

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// The scripts are written once and run in the engine layer and in the binary
// layer. A script package that imported the engine would tie every script to
// the engine layer and keep the binary layer's fixture, which must not import
// the engine, from implementing the same environment.

func TestScenarioPackageDoesNotImportTheEngineOrAFixtureOfAnyLayer(t *testing.T) {
	root := moduleRoot(t)
	module := modulePath(t, root)
	forbidden := []string{
		module + "/internal/node/engine",
		module + "/internal/node/testkit/harness",
		module + "/internal/node/testkit/fakeagent/engineagent",
	}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, tags := range []string{"", "crashtest,testclock,acceptance"} {
			t.Run(goos+"/tags="+tags, func(t *testing.T) {
				env := append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
				listing := goList(t, root, env, tags, []string{
					"-deps", "-f", `{{.ImportPath}}`, module + "/internal/node/testkit/scenario",
				})
				deps := strings.Fields(listing)
				if !slices.Contains(deps, module+"/internal/node/testkit/scenario") {
					t.Fatal("the scenario package is not in the dependency listing: it does not exist or is not buildable")
				}
				for _, f := range forbidden {
					if slices.Contains(deps, f) {
						t.Errorf("the scenario package depends on %s", f)
					}
				}
			})
		}
	}
}
