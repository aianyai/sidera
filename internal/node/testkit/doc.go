// Package testkit is the root of the test tooling of the runtime packages: the
// stand-ins for what lies outside the daemon (an adapter, a native runtime, the
// ledger of what happened), the fake agent interface and the crash-walk core.
// The package itself holds no code. README.md describes the layers, the shared
// test clock and the rules every package below follows.
package testkit
