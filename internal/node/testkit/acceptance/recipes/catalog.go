// Package recipes defines acceptance actions and judgements independently of a
// fixture. Construct a fresh catalog before every backend run: closures keep the
// results of that run only.
package recipes

import (
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

// Catalog separates scripts from the daemon's actual lifecycle entry points.
type Catalog struct {
	Scripts  []ScriptRecipe
	Startups []StartupRecipe
}

// ScriptRecipe adds a repeatable drain and a domain judgement to a script.
type ScriptRecipe struct {
	Script scenario.Script
	Drain  func(scenario.Env) error
	Check  func(scenario.Env) error
}

type StartupKind uint8

const (
	Init StartupKind = iota + 1
	Start
)

// StartupRecipe describes actual Init or Start, ending at the complete boundary.
type StartupRecipe struct {
	Name       string
	Kind       StartupKind
	StartAfter time.Duration
	MaxPoints  int
	Recovery   []crashwalk.RecoveryCase
}

// New constructs closures without invoking bindings or creating resources.
func New(b Bindings) Catalog {
	scripts := deliveryRecipes(b)
	scripts = append(scripts, sessionRecipes(b)...)
	return Catalog{Scripts: scripts, Startups: startupRecipes()}
}
