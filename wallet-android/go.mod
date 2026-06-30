// Separate Go module for the Android (gomobile) binding. It depends ONLY on the
// existing coin module ("cereblix") via a local replace, so the coin module
// stays zero-extra-deps. gomobile bind pulls in golang.org/x/mobile at BUILD
// time (see BUILD.md: `go get golang.org/x/mobile/bind`); that is a tool-only
// dependency of THIS module, never of the coin module.
//
// FINAL LOCATION: repos\cereblix\wallet-android, so the coin module ("cereblix")
// is one directory up. gomobile bind is run from this directory against ./mobile.
module cereblix-mobile

go 1.25.0

require cereblix v0.0.0

require (
	go.etcd.io/bbolt v1.5.0 // indirect
	golang.org/x/mobile v0.0.0-20260611195102-4dd8f1dbf5d2 // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/tools v0.46.0 // indirect
)

replace cereblix => ../
