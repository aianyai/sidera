package cli

import "path/filepath"

// programPath is the absolute path of this program as the commands that name it use
// it: the program as it was launched, with links kept, and only when the launch path is
// not known what the system reports for the running executable, made absolute.
//
// It is the file `update` replaces in place. A client's configuration that names it
// (the MCP server a setup registers, the command `setup controller` prints) therefore
// stays valid across an update, a link that a package manager keeps pointing at the
// current version keeps doing so, and setup and a later refresh pass the same string.
// The system's own answer can differ from it (on Linux it has the links resolved, and
// then names the file behind a link that a package manager repoints), which is why it
// is only the fallback.
func (c *command) programPath() (string, error) {
	if c.LaunchErr == nil && c.Launch.Program != "" {
		return c.Launch.Program, nil
	}
	path, err := c.Executable()
	if err == nil && !filepath.IsAbs(path) {
		path, err = filepath.Abs(path)
	}
	if err != nil {
		return "", &failure{"cannot find the program that is running: " + err.Error()}
	}
	return path, nil
}
