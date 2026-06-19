package node

import (
	"crypto/ed25519"
	"testing"
	"time"

	"cereblix/core"
)

// getTemplate must serve a stable job within templateRefresh, then be willing to
// rebuild from the live mempool afterwards, and it must NOT mint a new work id when
// the block body is unchanged (so miners are never interrupted for nothing).
// End-to-end transaction inclusion is exercised in production; this locks down the
// cache machinery the empty-block fix introduced so it cannot silently regress.
func TestGetTemplateCacheRefresh(t *testing.T) {
	ch, err := core.NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	n := &Node{
		Chain:      ch,
		templates:  map[string]*workTemplate{},
		tmplLatest: map[string]string{},
	}
	pub, _, _ := ed25519.GenerateKey(nil)
	addr := core.AddrFromPub(pub)

	b1, id1, err := n.getTemplate(addr)
	if err != nil {
		t.Fatal(err)
	}
	if len(b1.Txs) != 1 {
		t.Fatalf("empty mempool should yield a coinbase-only template, got %d txs", len(b1.Txs))
	}
	if id1 == "" {
		t.Fatal("work id must not be empty")
	}

	// Within templateRefresh -> same job, no rebuild (stable work for miners).
	if _, id2, _ := n.getTemplate(addr); id2 != id1 {
		t.Fatalf("work id must be stable within templateRefresh: %q vs %q", id1, id2)
	}

	// Age the job past templateRefresh. The body is still identical (empty mempool),
	// so the work id must NOT change (no needless churn) and the freshness timer must
	// be reset so we don't rebuild on every subsequent call.
	n.templates[id1].born = time.Now().Add(-templateRefresh - time.Second)
	if _, id3, _ := n.getTemplate(addr); id3 != id1 {
		t.Fatalf("unchanged body must keep the same work id: %q vs %q", id1, id3)
	}
	if time.Since(n.templates[id1].born) >= templateRefresh {
		t.Fatal("freshness timer should have been reset on the unchanged-body recheck")
	}

	// A late submit references its work id directly; the current job must still be
	// resolvable in the map for block reconstruction.
	if n.templates[id1] == nil {
		t.Fatal("current job must stay in the map for submit reconstruction")
	}
}

// getTemplate must reject an invalid coinbase address before touching any state.
func TestGetTemplateRejectsBadAddr(t *testing.T) {
	ch, err := core.NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	n := &Node{Chain: ch, templates: map[string]*workTemplate{}, tmplLatest: map[string]string{}}
	if _, _, err := n.getTemplate("not-a-valid-address"); err == nil {
		t.Fatal("expected an error for a bad coinbase address")
	}
}
