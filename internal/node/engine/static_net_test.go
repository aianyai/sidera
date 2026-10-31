//go:build netacceptance

package engine_test

// testOnlyMethods are the methods only a build with the netacceptance tag adds to
// the engine (testtrust.go).
var testOnlyMethods = []string{"TestInstallCredential", "TestOfferCredential"}
