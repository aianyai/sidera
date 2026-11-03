# adapterkit/realpath

The path for an existing file or directory in the form a program that resolves its
own working directory or its arguments ends up with: symbolic links resolved, and on
Windows also the platform's own case and long form. A path of an adapter that is
compared with one of the program's (a sandbox rule naming a directory, for
instance) has to be resolved the same way first.

It imports the standard library and, on Windows, `golang.org/x/sys/windows`.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Resolve(path)` | The absolute path of the existing file or directory at `path`; an error that is `fs.ErrNotExist` when there is none. |

## Rules

- **Elsewhere than Windows** it is `filepath.EvalSymlinks` of the absolute path:
  symbolic links are followed, the case of the names is kept as given. The case is
  the platform's own resolution only on Windows; on a case-insensitive macOS volume
  `/USERS/me` stays as written although a working directory reports `/Users/me`,
  so a caller that compares case-sensitively there has to be given the case itself.
- **On Windows** the file is opened without asking for any access and the path is
  read back with `GetFinalPathNameByHandle`. That follows every symbolic link and
  junction, and gives each name in its on-disk case and its long form, never the
  8.3 short name. That is the spelling the system itself reports to a program
  that asks it for a path, which a lexical or link-by-link walk does not promise.
- **The prefix is removed**: `\\?\C:\dir` is returned as `C:\dir` and
  `\\?\UNC\srv\share\dir` as `\\srv\share\dir`. A volume with no drive letter has no
  such form and is an error.
- **The path must exist.** Resolving the part of a path that does not exist yet is
  the caller's to do, on its existing parent.
