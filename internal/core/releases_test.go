package core

import "testing"

func TestLatestStableIgnoresPrereleaseAndOrder(t *testing.T) {
	releases := []Release{{Version: "26.10.17", Prerelease: true}, {Version: "26.3.27"}, {Version: "26.7.28", Prerelease: true}, {Version: "25.12.8"}, {Version: "26.3.9"}}
	if v, err := LatestStable(releases); err != nil || v != "26.3.27" {
		t.Fatal("latest stable mismatch", v, err)
	}
	if _, err := LatestStable([]Release{{Version: "26.10.17", Prerelease: true}}); err == nil {
		t.Fatal("prerelease-only list accepted")
	}
	if _, err := LatestStable(nil); err == nil {
		t.Fatal("empty list accepted")
	}
}
