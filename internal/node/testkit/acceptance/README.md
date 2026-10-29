# Acceptance

Acceptance recipes describe domain actions and evidence independently of their
execution layer. Read the parent [testkit README](../README.md) first.

- [recipes](recipes/README.md) constructs fresh scripts and lifecycle descriptions.
- [inproc](inproc/README.md) binds them to the existing in-process crash backend.

Recipes use `scenario.Env` and named domain callbacks for operations it does not
provide. The binding resolves the current process on every call. Neither a fixture
nor an engine handle belongs in the recipe API. No production binary imports this
subtree.
