// Package credential defines the signed records that carry trust between
// nodes: the mutual-trust credential, the revocation of one, the online voucher
// and the offline administrative directive. Each is a record type of package
// record; this package supplies the payloads, the descriptors and the facts
// that package trust and package admin read, and decides nothing that either of
// them decides. See README.md for the rules.
package credential
