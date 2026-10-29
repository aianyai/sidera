package recipes

import "time"

func startupRecipes() []StartupRecipe {
	return []StartupRecipe{
		{Name: "startup-init", Kind: Init, MaxPoints: 83},
		{Name: "startup-renew", Kind: Start, StartAfter: 21 * 24 * time.Hour, MaxPoints: 17},
	}
}
