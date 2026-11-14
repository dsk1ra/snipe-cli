package scan

import (
	"os"
	"strings"
	"testing"
)

// Location strings as the boards send them, most of them from scan-history.tsv
// and the first full scan with the registry.
func TestPlace(t *testing.T) {
	const (
		L, WM, UK, W, X = InLondon, InWestMidlands, InUK, UKWide, Abroad
	)
	for loc, want := range map[string]Where{
		"London":                          L,
		"London, England, United Kingdom": L,
		"London EC2A4RX":                  L,
		"City Of Westminster, England, United Kingdom": L,
		"UK - London": L,
		"Canary Wharf, 1 Churchill Place, United Kingdom":                       L,
		"Putney Office, Putney, Greater London, SW15 2TG":                       L,
		"Clapham Junction, United Kingdom":                                      L,
		"Canada Water":                                                          L, // not Canada
		"Hybrid - San Francisco, New York City, London":                         L | X,
		"Cardiff, London or Remote (UK)":                                        L | UK | W,
		"Berlin Office; London Office; Remotely in Germany; Remotely in the UK": L | W | X,

		"Birmingham":       WM,
		"Coventry":         WM, // matched no keyword before the gazetteer
		"Sutton Coldfield": WM, // not Sutton, London
		"Birmingham, One Snow Hill, United Kingdom": WM,
		"B4 6AT": WM,
		"Tewkesbury, GBR; Wolverhampton, GBR, United Kingdom": WM | UK,

		"Warwick, CV34 6DA":     UK, // the region, not the metropolitan county
		"B60 1AA":               UK, // Bromsgrove
		"Rugby, United Kingdom": UK, // a country beside a town is not UK-wide
		"Manchester Westminster House, United Kingdom": UK, // Westminster is a building
		"Stratford-upon-Avon":                          UK,
		"Edinburgh / Hybrid":                           UK,
		"Belfast, Northern Ireland":                    UK, // not Ireland
		"Edinburgh or Remote (UK)":                     UK | W,
		"Chippenham Area, United Kingdom":              UK, // a town the list does not know
		"Welcome Building, United Kingdom":             UK, // nor an office

		"United Kingdom":                            W,
		"Remote - United Kingdom":                   W,
		"UK Home Based, Home Based, United Kingdom": W,
		"Washington, England":                       W | X,

		"Birmingham, AL":          X,
		"Birmingham Alabama":      X,
		"London, Ontario":         X,
		"London, ON":              X,
		"Sydney, New South Wales": X, // not Wales
		"New York, NY":            X,
		"Dublin, Ireland":         X,
		"IND - Telangana - Hyderabad Hitec City Raidurg":          X,
		"Working From Home, CALIFORNIA, United States of America": X,

		"Remote":           0,
		"EMEA":             0,
		"Welcome Building": 0,
	} {
		if got := Place(loc); got != want {
			t.Errorf("Place(%q) = %v (%s), want %v (%s)", loc, got, got.Label(), want, want.Label())
		}
	}
}

func TestLocationOKWithRegions(t *testing.T) {
	c := cfg(t, "location_filter:\n  regions: [london, west-midlands]\n  allow: [remote]\n")
	for loc, want := range map[string]bool{
		"Coventry":                true,
		"London, ON":              false, // placed abroad, though it says London
		"Rugby, United Kingdom":   false, // in the UK, outside both regions
		"Remote - United Kingdom": false, // UK-wide is not listed
		"Glasgow; London":         true,  // one option is in a region
		"Remote":                  true,  // placed nowhere: the keyword tiers decide
		"Welcome Building":        false, // placed nowhere, no keyword
		"Sydney, New South Wales": false,
	} {
		if got := c.LocationOK(loc); got != want {
			t.Errorf("LocationOK(%q) = %v, want %v", loc, got, want)
		}
	}
	if uk := cfg(t, "location_filter:\n  regions: [uk]\n"); !uk.LocationOK("Rugby, United Kingdom") || uk.LocationOK("Dublin, Ireland") {
		t.Error("regions: [uk] should keep all of the UK and nothing abroad")
	}
	// The setting the Location Policy implies: the two regions in any form,
	// elsewhere in the UK only when nothing ties the posting to a town.
	policy := cfg(t, "location_filter:\n  regions: [london, west-midlands, uk-wide]\n")
	for loc, want := range map[string]bool{
		"Remote - United Kingdom":        true,
		"Edinburgh or Remote (UK)":       true,
		"Rugby, United Kingdom":          false,
		"Glasgow Campus, United Kingdom": false,
		"Solihull":                       true,
	} {
		if got := policy.LocationOK(loc); got != want {
			t.Errorf("policy LocationOK(%q) = %v, want %v", loc, got, want)
		}
	}
	p := t.TempDir() + "/portals.yml"
	if err := writeFile(p, "location_filter:\n  regions: [west midlands]\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err == nil || !strings.Contains(err.Error(), "west midlands") {
		t.Errorf("a misspelt region should fail the load, got %v", err)
	}
}

func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o644) }
