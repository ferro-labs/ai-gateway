package capabilities

// Matrix exposes the unexported matrix to the drift guard in package
// capabilities_test.
//
// The guard has to live in the external test package because it imports
// providers, and providers imports this package (through core) — an in-package
// test importing providers would be an import cycle. This file imports nothing,
// so it bridges the two without creating one.
var Matrix = matrix
