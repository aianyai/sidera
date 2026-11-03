# unpack

Extracts a release archive (`.tar.gz` on macOS and Linux, `.zip` on Windows) into an
empty directory. `Extract` is the only entry point; `DefaultLimits` are the bounds the
update command passes.

An archive is untrusted input even when its checksum matched, because the checksum shows
who built it, not that writing its members is safe. So `Extract` writes only regular files
and directories, and refuses (`ErrRefused`):

- a name that is not a plain relative path: absolute, with a `..` or `.` component, an
  empty component, a backslash, a colon or a NUL byte (Windows drive and UNC forms are
  covered by the last three);
- a symbolic or hard link, a device, a named pipe, or any other kind of member;
- a name that occurs twice, directories included;
- a file over `MaxMember`, files together over `MaxTotal`, more than `MaxEntries` members,
  or a member whose real size is not the size the archive declares.

The target directory must exist and be empty, so nothing below it existed before and no link
can lead out of it (the writes use `O_EXCL`). Modes are not copied from the archive: a file
with any executable bit becomes `0755`, any other file `0644`, a directory `0755`, so a
release's programs run and nothing is setuid or writable by others. Parent directories of a
file are made as needed; a release has no directory around its files.

A refused or damaged archive stops the extraction where the problem is found and leaves what
was written before. The caller owns the directory and removes it.

The release layout (`sidera`, `sidera-adapter-console`, `adapters/<name>/…`) is not checked
here: this package knows archives, not releases.
