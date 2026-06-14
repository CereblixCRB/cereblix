// cereblix-manifest builds and signs an UpgradeManifest with the authority key.
// The output JSON is what nodes fetch (GitHub / origin / relay) and verify
// against the hardcoded authority public key before auto-updating.
//
// Example:
//
//	cereblix-manifest -key /opt/cerebra/authority.key \
//	  -version 2.0.0 -minversion 2.0.0 -notes "fee market upgrade" \
//	  -fork feemarket:3195:2.0.0 \
//	  -bin linux-amd64:https://.../cereblixd-linux-amd64:<sha256> \
//	  -out upgrade.json
package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"cereblix/core"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(s string) error { *m = append(*m, s); return nil }

func main() {
	keyfile := flag.String("key", "/opt/cerebra/authority.key", "authority private key file (128-hex ed25519)")
	version := flag.String("version", "", "latest release version, e.g. 2.0.0")
	minver := flag.String("minversion", "", "minimum version nodes should run (defaults to -version)")
	notes := flag.String("notes", "", "human-readable release notes")
	out := flag.String("out", "", "output file (default stdout)")
	ghbase := flag.String("gh", "", "preferred binary mirror base (GitHub), e.g. https://github.com/OWNER/REPO/releases/latest/download")
	sitebase := flag.String("site", "", "fallback binary mirror base (our origin), e.g. https://cereblix.com")
	var forks, bins multiFlag
	flag.Var(&forks, "fork", "name:height:minversion (repeatable)")
	flag.Var(&bins, "bin", "platform:filename:sha256 (repeatable); URLs built from -gh (first) and -site (fallback)")
	flag.Parse()

	if *version == "" {
		log.Fatal("-version is required")
	}
	if *minver == "" {
		*minver = *version
	}

	raw, err := os.ReadFile(*keyfile)
	if err != nil {
		log.Fatalf("read key: %v", err)
	}
	sk, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(sk) != ed25519.PrivateKeySize {
		log.Fatal("bad authority key (need 128-hex ed25519)")
	}
	priv := ed25519.PrivateKey(sk)

	m := core.UpgradeManifest{
		Version:    *version,
		MinVersion: *minver,
		Notes:      *notes,
		Binaries:   map[string]core.UpgradeBinary{},
	}
	gh := strings.TrimRight(*ghbase, "/")
	site := strings.TrimRight(*sitebase, "/")
	for _, b := range bins {
		// platform:filename:sha256 (no colons in any field). Build the mirror list:
		// GitHub first (preferred), origin as fallback.
		p := strings.Split(b, ":")
		if len(p) != 3 {
			log.Fatalf("bad -bin %q (want platform:filename:sha256)", b)
		}
		platform, filename, sha := p[0], p[1], p[2]
		var urls []string
		if gh != "" {
			urls = append(urls, gh+"/"+filename)
		}
		siteURL := ""
		if site != "" {
			siteURL = site + "/" + filename
			urls = append(urls, siteURL)
		}
		m.Binaries[platform] = core.UpgradeBinary{URLs: urls, URL: siteURL, SHA256: sha}
	}
	for _, f := range forks {
		p := strings.SplitN(f, ":", 3)
		if len(p) != 3 {
			log.Fatalf("bad -fork %q (want name:height:minversion)", f)
		}
		h, err := strconv.ParseUint(p[1], 10, 64)
		if err != nil {
			log.Fatalf("bad fork height in %q: %v", f, err)
		}
		m.Forks = append(m.Forks, core.UpgradeFork{Name: p[0], Height: h, MinVersion: p[2]})
	}

	signed := core.SignManifest(m, priv)
	if !signed.Verify() {
		log.Fatal("self-check failed: signed manifest does not verify against the hardcoded authority pubkey - wrong key file?")
	}
	js, _ := json.MarshalIndent(signed, "", "  ")
	if *out == "" {
		fmt.Println(string(js))
		return
	}
	if err := os.WriteFile(*out, js, 0o644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}
	fmt.Printf("wrote %s (v%s, %d binaries, %d forks)\n", *out, signed.Version, len(signed.Binaries), len(signed.Forks))
}
