# config

The settings of one adapter instance, decoded from the flat values the daemon hands
over (`Decode`), and the alias table (`ParseAliases`, `Aliases.Lookup`, and `Aliases.NameOf` to show a sender by its alias).

- Every key is a setting or an error; defaults are named constants with the reason for
  the number.
- The alias table is the allow-list of everything a card can make the node do. Names
  are lowercase. Adding a mode or a field to an alias changes what a card may do:
  update the reference.
- A profile name is checked against Hermes's rule (`ValidateProfileName`) wherever the
  owner gives one (`home_profile`, `creator_profiles`, and `setup --profile`): it is passed
  as the value of `-p`, and one beginning with a dash would be read as an option.
- `Target` is the set of fields of a Hermes notify subscription; the state package
  keeps it with each endpoint.
- Settings must be declared in the package manifest with the same keys and defaults
  (`internal/adapter/manifest.go`; its test decodes the declared defaults).
