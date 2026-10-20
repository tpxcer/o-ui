package config

import (
	"strings"
	"testing"
)

func TestSniffingExclusions(t *testing.T) {
	valid := Sniffing{IPsExcluded: []string{"192.0.2.1", "192.0.2.0/24", "2001:db8::/32", "::1"}, DomainsExcluded: []string{"example.com", "domain:example.com", "full:example.org"}}
	if err := ValidateSniffing(valid); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []Sniffing{
		{IPsExcluded: []string{"192.0.2.1/100"}}, {IPsExcluded: []string{"geoip:cn"}}, {IPsExcluded: []string{"ext:/tmp/ip.dat:cn"}},
		{DomainsExcluded: []string{"ext:/tmp/domains.dat:cn"}}, {DomainsExcluded: []string{"https://example.com"}}, {DomainsExcluded: []string{"a\x00b"}}, {DomainsExcluded: []string{strings.Repeat("a", 257)}}, {IPsExcluded: make([]string, 65)},
		{DomainsExcluded: []string{"domain:full:example.com"}}, {DomainsExcluded: []string{"full:domain:example.com"}},
	} {
		if ValidateSniffing(invalid) == nil {
			t.Fatal("invalid exclusion accepted")
		}
	}
}
