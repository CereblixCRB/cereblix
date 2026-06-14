package neuromorph

import (
	"encoding/hex"
	"testing"
)

func TestDeterminism(t *testing.T) {
	p := DeriveParams(EpochSeed0())
	vm1 := NewVM(p)
	vm2 := NewVM(p)
	header := make([]byte, 124)
	for i := range header {
		header[i] = byte(i * 7)
	}
	h1 := vm1.Hash(header, 0)
	h2 := vm2.Hash(header, 0)
	if h1 != h2 {
		t.Fatalf("non-deterministic: %x vs %x", h1, h2)
	}
	// Same VM reused must give the same answer (buffer reset correctness).
	h3 := vm1.Hash(header, 0)
	if h1 != h3 {
		t.Fatalf("vm reuse changes result: %x vs %x", h1, h3)
	}
	header[5] ^= 1
	h4 := vm1.Hash(header, 0)
	if h4 == h1 {
		t.Fatal("hash ignores input changes")
	}
	t.Logf("nm hash (pre-dataset): %s", hex.EncodeToString(h1[:]))

	// Post-activation: the dataset step must be deterministic, shared, and must
	// change the result versus the pre-activation hash of the same header.
	d1 := NewVM(p).Hash(header, DatasetHeight)
	d2 := NewVM(p).Hash(header, DatasetHeight)
	if d1 != d2 {
		t.Fatalf("dataset path non-deterministic: %x vs %x", d1, d2)
	}
	if d1 == vm1.Hash(header, 0) {
		t.Fatal("dataset step did not change the hash")
	}
	t.Logf("nm hash (with dataset): %s", hex.EncodeToString(d1[:]))
}

// TestCrossPlatformHash pins the epoch-0 hash of a fixed header. It must hold
// identically on every consensus platform (amd64, arm64, wasm); a mismatch
// means that platform would mine invalid blocks, so a miner for it must not
// ship until this passes there.
func TestCrossPlatformHash(t *testing.T) {
	p := DeriveParams(EpochSeed0())
	header := make([]byte, 124)
	for i := range header {
		header[i] = byte(i * 7)
	}
	h := NewVM(p).Hash(header, 0)
	got := hex.EncodeToString(h[:])
	const want = "9748a3aa3d3b7c331585171b42297234830be0ec90e1ecd4425717f631c00aa7"
	if got != want {
		t.Fatalf("PLATFORM HASH MISMATCH - do not mine on this platform\n got %s\nwant %s", got, want)
	}
}

func TestEpochsDiffer(t *testing.T) {
	p0 := DeriveParams(EpochSeed0())
	p1 := DeriveParams([]byte("some other epoch boundary hash..32b"))
	if p0.ProgSize == p1.ProgSize && p0.Loops == p1.Loops && p0.RotSalt == p1.RotSalt {
		t.Fatal("epoch params do not vary")
	}
}

func BenchmarkHash(b *testing.B) {
	p := DeriveParams(EpochSeed0())
	vm := NewVM(p)
	header := make([]byte, 124)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		header[120] = byte(i)
		vm.Hash(header, 0)
	}
}

func BenchmarkHashDataset(b *testing.B) {
	p := DeriveParams(EpochSeed0())
	vm := NewVM(p)
	header := make([]byte, 124)
	vm.Hash(header, DatasetHeight) // warm/generate the dataset
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		header[120] = byte(i)
		vm.Hash(header, DatasetHeight)
	}
}
