# fetch

How `sidera update` reaches GitHub: it finds the latest release and downloads one
asset of a release. It checks no signature and reads no file; whatever it returns
is untrusted until the caller has verified it, which is why it does not pin a host.

## Entry points

| Name | What it is for |
| ---- | -------------- |
| `New(userAgent)` | The client for `https://github.com/aianyai/sidera`, with the proxy read from the environment |
| `NewAt(base, transport, userAgent)` | The same policy against another base URL and transport; for tests that serve a repository from a local TLS server. Nothing a user can set reaches it |
| `UserAgent(version)` | The text every request carries: `sidera/<version> (<goos>/<goarch>)` |
| `Client.Latest(ctx)` | The latest version, without the leading `v`, read from the redirect of `<repository>/releases/latest` |
| `Client.ReleaseURL(version)` | The page of a release, `<repository>/releases/tag/v<version>`: where the redirect of the latest release leads, and what a person is shown to read about it |
| `Client.Download(ctx, version, asset, w, limit)` | Writes the asset `releases/download/v<version>/<asset>` to `w`, at most `limit` bytes |
| `MaxChecksums`, `MaxSignature`, `MaxArchive` | The limits for the three kinds of object: 64 KiB, 4 KiB, 256 MiB |
| `ErrNotFound`, `ErrTooLarge` | What `errors.Is` finds in a 404 and in a body past its limit |

## Rules

- `Latest` does not follow the redirect: the `Location` is the answer. Only a 302
  counts, and the `Location` must be exactly `<repository>/releases/tag/v<version>`
  with `<version>` in the release version syntax (`release.ParseVersion`: a
  `sdk/go/semver` version without build metadata, `1.2.3` or `1.2.3-rc.1`).
  Anything else is an error that shows the `Location`.
- `Download` follows at most 5 redirects and refuses a hop that is not HTTPS. It
  accepts any host: GitHub serves assets from a storage host that has changed
  before, and the signature, not the host, makes a file authentic.
- A version or asset name that is not a plain release version or file name is
  refused before any request, so nothing a response says reaches a URL path
  unchecked.
- The limit applies to the body: an announced length past it fails before a byte
  is written; otherwise `w` receives at most `limit` bytes and the download fails
  at the first byte past it. A failed download may leave a prefix in `w`; the
  caller discards what it wrote.
- Each request has one deadline that covers the body: one minute for finding the
  latest release and for a download whose limit is at most `MaxChecksums`, fifteen
  minutes for a larger one. The transport adds a 30 s limit on the response header
  to the dial (30 s) and TLS (10 s) limits of the standard library.
- Requests carry no credential. Errors name what failed and the host that
  answered, never the URL of a redirect, whose query may be a signed token.
- Only the CLI uses this package, when it runs `update`; the daemon never contacts
  GitHub or any release server.
