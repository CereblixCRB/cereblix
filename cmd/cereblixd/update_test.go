package main

import "testing"

func TestDecideBoot(t *testing.T) {
	cases := []struct {
		name string
		in   bootState
		want bootDecision
	}{
		{"env broken -> env problem", bootState{EnvOK: false, HasPending: true, Attempts: 9, HasOld: true}, bootEnvProblem},
		{"clean boot -> normal", bootState{EnvOK: true}, bootNormal},
		{"pending within budget -> watch", bootState{EnvOK: true, HasPending: true, Attempts: 1, HasOld: true}, bootWatch},
		{"pending at budget edge -> watch", bootState{EnvOK: true, HasPending: true, Attempts: maxBadBoots - 1, HasOld: true}, bootWatch},
		{"crash-loop with backup -> rollback", bootState{EnvOK: true, HasPending: true, Attempts: maxBadBoots, HasOld: true}, bootRollback},
		{"crash-loop no backup -> give up", bootState{EnvOK: true, HasPending: true, Attempts: maxBadBoots, HasOld: false}, bootGiveUp},
		{"rollback target boot -> confirm", bootState{EnvOK: true, HasSuspect: true, ConfirmAttempts: 1}, bootConfirmRollback},
		{"rollback target also crash-loops -> env fault", bootState{EnvOK: true, HasSuspect: true, ConfirmAttempts: maxBadBoots}, bootEnvFaultConfirmed},
		{"pending beats suspect", bootState{EnvOK: true, HasPending: true, Attempts: 1, HasSuspect: true, ConfirmAttempts: 9}, bootWatch},
	}
	for _, c := range cases {
		if got := decideBoot(c.in); got != c.want {
			t.Errorf("%s: decideBoot(%+v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

func TestShouldInstall(t *testing.T) {
	cases := []struct {
		manifest, node, blocked string
		want                    bool
	}{
		{"2.0.1", "2.0.0", "", true},       // newer, nothing blocked -> install
		{"2.0.0", "2.0.0", "", false},      // same -> no
		{"2.0.0", "2.0.1", "", false},      // older -> no (no downgrade)
		{"2.0.1", "2.0.0", "2.0.1", false}, // exactly the blocked version -> no
		{"2.0.1", "2.0.0", "2.0.2", false}, // not newer than blocked -> no
		{"2.0.2", "2.0.0", "2.0.1", true},  // newer than blocked bad -> install the fix
		{"2.1.0", "2.0.0", "2.0.5", true},  // fix well past the bad one -> install
	}
	for _, c := range cases {
		if got := shouldInstall(c.manifest, c.node, c.blocked); got != c.want {
			t.Errorf("shouldInstall(%q,%q,%q)=%v want %v", c.manifest, c.node, c.blocked, got, c.want)
		}
	}
}

func TestVerNewer(t *testing.T) {
	yes := [][2]string{{"2.0.1", "2.0.0"}, {"2.1.0", "2.0.9"}, {"2.10.0", "2.9.0"}, {"3.0.0", "2.9.9"}}
	no := [][2]string{{"2.0.0", "2.0.0"}, {"2.0.0", "2.0.1"}, {"2.9.0", "2.10.0"}}
	for _, p := range yes {
		if !verNewer(p[0], p[1]) {
			t.Errorf("verNewer(%q,%q) should be true", p[0], p[1])
		}
	}
	for _, p := range no {
		if verNewer(p[0], p[1]) {
			t.Errorf("verNewer(%q,%q) should be false", p[0], p[1])
		}
	}
}
