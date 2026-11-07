# hostfacts

What this machine is: its host name, its platform, and the operating system it
runs, as a label for people and as a version string for machines. It validates
nothing against display rules and knows nothing of records: whoever publishes
the facts checks them.

## Entry points

| Name | What it is for |
| ---- | -------------- |
| `Read()` | The `Facts` of this machine. It never fails and never panics: a field it could not read is `""`, and `Facts.Problems` says why, for a debug log. |
| `Facts.HostName` | The first DNS label of `os.Hostname()`: `imac` for `imac.local`. Read on every call. |
| `Facts.Platform` | `runtime.GOOS + "/" + runtime.GOARCH`, such as `darwin/arm64`. |
| `Facts.System` | The operating system as a person names it: `macOS 27.0.1`, `Ubuntu 24.04.1 LTS`, `Windows 11 24H2`. |
| `Facts.SystemBuild` | The machine version: `26A434` on macOS, the kernel release on Linux, `10.0.26100.4061` on Windows. |

The system fields are read once per process, because they do not change while
the daemon runs. The host name is read on every call, because the owner can
rename the machine.

## Where each value comes from

- **macOS**: `kern.osproductversion` (label `macOS <version>`) and
  `kern.osversion` (build), through `unix.Sysctl`. The Go linker marks binaries
  with a recent SDK, so the product version is never the compatibility `10.16`.
- **Linux**: `PRETTY_NAME` from `/etc/os-release`, or from `/usr/lib/os-release`
  only when the first file does not exist (a dangling link counts as not
  existing); the two are never merged. The files are read through
  `fsx.OpenOSReader("/")` and `ReadFileLimit` with a 64 KiB bound, which follows
  the link that `/etc/os-release` usually is. Without `PRETTY_NAME` the label is `NAME` followed by `VERSION`; without `NAME`, or
  without either file, it is `Linux`. A file that exists but cannot be read
  gives `Linux` and a problem. The build is `unix.Uname`'s kernel release. In a
  container `os-release` describes the image and the kernel is the host's.
- **Windows**: `windows.RtlGetNtVersionNumbers` for major, minor and build.
  It answers truthfully to a program without a compatibility manifest, where
  `GetVersionEx` says 6.2, and it masks the flag bits off the build number. Major
  10 with a build of 22000 or more is `Windows 11`, below that `Windows 10`;
  any other major is `Windows <major>.<minor>`. `DisplayVersion` (`24H2`) from
  `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion`, read in the 64-bit view,
  follows the label; `UBR` follows the build as `.<revision>`. Either value being
  absent only shortens the string.
- **Other systems**: `System` and `SystemBuild` are `""`.

## Invariants

- **Files are read only through the read-only part of `fsx`,** as in every runtime
  package; the layout test enforces it. The system calls, the registry and the
  sysctls are not file functions.
- **No program is run and there is no cgo.** `sw_vers` and `scutil` would start a
  process to learn what a system call already says, and cgo would break the
  cross-compilation the release builds rely on. The only dependencies are
  `golang.org/x/sys` and `fsx`.
- **The host name is the DNS label, on every platform.** The domain a network
  adds (`imac.local`) changes with the network and says nothing about the
  machine. macOS's `ComputerName` (the name the owner sees in Settings) is not
  read: it needs `scutil` or cgo.
- **The registry's `ProductName` is not used.** It says `Windows 10` on Windows
  11; the build number is the reliable signal.
- **Parsing and labelling are separate from the system calls.** `parseOSRelease`,
  `linuxLabel` (which takes the file reader as a parameter, and paths relative to the root of the file system, as `fsx` does), `windowsLabel`,
  `windowsBuild` and `firstLabel` build on every platform and are tested on every
  platform; the system calls themselves (`system_<goos>.go`) are not.
