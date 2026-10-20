package scan

import (
	"regexp"
	"strconv"
	"strings"
)

// Where is the set of places a location string names. A multi-location posting
// can be in several at once: "Birmingham; London" is both.
type Where uint8

const (
	InLondon Where = 1 << iota
	InWestMidlands
	InUK   // a named place in the UK outside the two above: Rugby, Glasgow
	UKWide // the UK with no town named, or an option that is remote in it
	Abroad
)

// Placed is everything that puts a posting in the UK.
const Placed = InLondon | InWestMidlands | InUK | UKWide

// Label names where a posting is, for the scan's output.
func (w Where) Label() string {
	var parts []string
	if w&InWestMidlands != 0 {
		parts = append(parts, "West Midlands")
	}
	if w&InLondon != 0 {
		parts = append(parts, "London")
	}
	switch {
	case len(parts) > 0:
		return strings.Join(parts, " + ")
	case w&UKWide != 0:
		return "UK-wide"
	case w&InUK != 0:
		return "elsewhere in the UK"
	case w&Abroad != 0:
		return "abroad"
	}
	return "unplaced"
}

// regionBits are the names location_filter.regions accepts.
var regionBits = map[string]Where{
	"london": InLondon, "west-midlands": InWestMidlands, "uk-wide": UKWide, "uk": Placed,
}

// A part of a location string, once matched, is one of these.
type kind uint8

const (
	none      kind = iota
	london         // a place in London
	westMids       // a place in the West Midlands metropolitan county
	ukPlace        // a place elsewhere in the UK
	ukMarker       // "United Kingdom", "England", "GBR": the UK, nowhere in particular
	abroad         // a city outside the UK
	qualifier      // a country, state or province outside the UK
)

// The gazetteer. West Midlands is the metropolitan county — Birmingham,
// Coventry, Wolverhampton, Solihull, Dudley, Walsall, Sandwell — not the wider
// region: Warwick, Rugby and Stafford are elsewhere in the UK. London carries no
// name that is also a place elsewhere (Sutton, Richmond, Stratford): those lose
// to the longer name they share a word with, or are left out.
//
// ponytail: a hand-written list, a few hundred names. The ONS places index is
// the upgrade if postings start naming towns it misses.
var places = func() map[string]kind {
	m := map[string]kind{}
	add := func(k kind, names string) {
		for _, n := range strings.Split(names, ",") {
			if n = norm(n); n != "" {
				m[n] = k
			}
		}
	}
	add(ukMarker, `united kingdom, uk, gb, gbr, great britain, britain, england, scotland, wales,
		northern ireland, cymru`)
	add(london, `london, city of london, greater london, westminster, city of westminster, canary wharf,
		docklands, shoreditch, holborn, soho, mayfair, marylebone, kings cross, paddington, euston,
		farringdon, clerkenwell, islington, camden, hackney, southwark, lambeth, wandsworth, battersea,
		putney, hammersmith, fulham, chelsea, kensington, white city, greenwich, north greenwich,
		croydon, bromley, ealing, harrow, hounslow, uxbridge, wembley, barnet, enfield, wimbledon,
		tower hamlets, west drayton, heathrow, kingston upon thames, richmond upon thames, old street,
		moorgate, liverpool street, aldgate, london bridge, elephant and castle, brixton, lewisham,
		woolwich, tottenham, walthamstow, ilford, romford, dagenham, twickenham, chiswick, brentford,
		hampstead, bermondsey, vauxhall, pimlico, knightsbridge, notting hill, covent garden, fitzrovia,
		bloomsbury, whitechapel, spitalfields, wapping, isle of dogs, royal docks, hayes, stanmore,
		clapham, clapham junction, canada water, barbican, blackfriars, bankside, south bank, southbank,
		waterloo, kentish town, nine elms, earls court, chancery lane, aldwych, hoxton, dalston,
		bethnal green, mile end, st johns wood, wood lane, regents place, kings place, fenchurch street`)
	add(westMids, `west midlands, birmingham, coventry, wolverhampton, solihull, dudley, walsall,
		sandwell, west bromwich, sutton coldfield, halesowen, stourbridge, oldbury, smethwick,
		brierley hill, bilston, willenhall, wednesbury, tipton, rowley regis, kingswinford, edgbaston,
		erdington, selly oak, digbeth, bournville, harborne, longbridge, kings norton, acocks green,
		meriden, balsall common, castle bromwich, chelmsley wood, knowle, dorridge, brindleyplace,
		brindley place, colmore, snow hill, jewellery quarter, blythe valley, fort dunlop, perry barr,
		great barr, handsworth, moseley, kings heath, hall green, yardley, warley`)
	add(ukPlace, `manchester, salford, salford quays, stockport, bolton, wigan, oldham, rochdale, bury,
		trafford, leeds, bradford, wakefield, huddersfield, halifax, harrogate, york, hull, sheffield,
		doncaster, rotherham, barnsley, liverpool, warrington, chester, crewe, runcorn, knutsford,
		macclesfield, newcastle, newcastle upon tyne, gateshead, sunderland, durham, middlesbrough,
		middlesborough, darlington, nottingham, derby, leicester, northampton, milton keynes, lincoln,
		peterborough, cambridge, oxford, reading, slough, bracknell, wokingham, maidenhead, newbury,
		basingstoke, farnborough, guildford, woking, weybridge, staines, egham, leatherhead, epsom,
		crawley, horsham, brighton, worthing, eastbourne, hastings, bexhill, chichester, southampton,
		portsmouth, winchester, bournemouth, poole, exeter, plymouth, truro, bristol, filton, nailsea,
		bath, swindon, cheltenham, gloucester, tewkesbury, wotton under edge, worcester, hereford,
		evesham, redditch, bromsgrove, kidderminster, stoke, stoke on trent, stafford, lichfield,
		tamworth, cannock, burton upon trent, telford, shrewsbury, warwick, leamington,
		leamington spa, kenilworth, rugby, nuneaton, stratford upon avon, gaydon, norwich, ipswich,
		colchester, chelmsford, southend, basildon, luton, watford, st albans, hatfield, stevenage,
		hemel hempstead, high wycombe, aylesbury, bedford, maidstone, canterbury, dover, ashford,
		tunbridge wells, sevenoaks, brize norton, cranleigh, melton mowbray, preston, blackpool,
		lancaster, carlisle, edinburgh, glasgow, aberdeen, dundee, inverness, stirling, livingston,
		bathgate, prestwick, paisley, cardiff, swansea, newport, wrexham, broughton, bangor, belfast,
		derry, londonderry`)
	add(qualifier, `united states, united states of america, usa, us, u s, america, canada, ireland,
		france, germany, netherlands, spain, portugal, italy, poland, india, singapore, australia,
		new zealand, japan, china, hong kong, taiwan, korea, south korea, brazil, mexico, argentina,
		colombia, chile, switzerland, sweden, denmark, norway, finland, belgium, austria, luxembourg,
		czech republic, czechia, romania, bulgaria, greece, turkey, israel, uae, united arab emirates,
		saudi arabia, qatar, egypt, south africa, nigeria, kenya, philippines, vietnam, thailand,
		malaysia, indonesia, pakistan, ukraine, lithuania, latvia, estonia, hungary, serbia, croatia,
		slovakia, slovenia, cyprus, malta, iceland, jersey, guernsey, isle of man,
		deu, fra, aus, irl, nld, esp, pol, sgp, che, swe, bel, ita, prt, rou, bgr, cze, hun, isr,
		bra, mex, jpn, chn, kor, phl, zaf,
		alabama, alaska, arizona, arkansas, california, colorado, connecticut, delaware, florida,
		georgia, hawaii, idaho, illinois, indiana, iowa, kansas, kentucky, louisiana, maine, maryland,
		massachusetts, michigan, minnesota, mississippi, missouri, montana, nebraska, nevada,
		new hampshire, new jersey, new mexico, new york, new york state, north carolina, north dakota,
		ohio, oklahoma, oregon, pennsylvania, rhode island, south carolina, south dakota, tennessee,
		texas, utah, vermont, virginia, washington, west virginia, wisconsin, wyoming,
		district of columbia, new england,
		ontario, quebec, british columbia, alberta, manitoba, nova scotia, saskatchewan,
		new south wales, queensland, tasmania, western australia, nsw,
		karnataka, telangana, maharashtra, tamil nadu, haryana, uttar pradesh, west bengal, kerala,
		gujarat, andhra pradesh`)
	add(abroad, `new york city, nyc, san francisco, seattle, boston, austin, chicago, los angeles,
		san jose, santa clara, palo alto, mountain view, sunnyvale, menlo park, denver, atlanta, dallas,
		houston, miami, phoenix, portland, philadelphia, pittsburgh, toronto, vancouver, montreal,
		ottawa, berlin, munich, hamburg, frankfurt, cologne, stuttgart, dusseldorf, paris, lyon,
		amsterdam, rotterdam, eindhoven, dublin, cork, galway, madrid, barcelona, valencia, lisbon,
		porto, warsaw, krakow, wroclaw, gdansk, prague, brno, zurich, geneva, lausanne, zug, basel,
		stockholm, gothenburg, copenhagen, oslo, helsinki, tallinn, vilnius, riga, bucharest, cluj,
		sofia, athens, milan, rome, turin, vienna, brussels, antwerp, budapest, belgrade, zagreb,
		tel aviv, bengaluru, bangalore, hyderabad, pune, chennai, mumbai, delhi, new delhi, noida,
		gurgaon, gurugram, kolkata, sydney, melbourne, brisbane, auckland, tokyo, shanghai, beijing,
		shenzhen, seoul, taipei, dubai, abu dhabi, riyadh, doha, manila, cape town, johannesburg,
		lagos, nairobi, cairo, sao paulo, mexico city, bogota, buenos aires, santiago, kyiv, lviv,
		istanbul, kuala lumpur, jakarta, bangkok, ho chi minh city, toulouse, schenectady, niskayuna,
		greenville, whippany`)
	return m
}()

// codes are the US state and Canadian and Australian province abbreviations
// that follow a city — "Birmingham, AL" — counted only as a whole part, where
// they cannot be an English word.
var codes = map[string]bool{}

func init() {
	for _, c := range strings.Fields(`al ak az ar ca co ct de fl ga hi id il in ia ks ky la me md ma mi mn
		ms mo mt ne nv nh nj nm ny nc nd oh ok or pa ri sc sd tn tx ut vt va wa wv wi wy dc
		on bc qc ab mb ns sk qld vic`) {
		codes[c] = true
	}
}

var (
	segmentSep = regexp.MustCompile(`\s*(?:[;|/]|\s[-–—]\s|\s+or\s+)\s*`)
	partSep    = regexp.MustCompile(`[,()\[\]]`)
	nonWord    = regexp.MustCompile(`[^a-z0-9]+`)
	postcode   = regexp.MustCompile(`(?i)\b([A-Z]{1,2})(\d{1,2})[A-Z]?\s*\d[A-Z]{2}\b`)
	remote     = regexp.MustCompile(`\b(?:remote|remotely|home based|homebased|work from home|wfh|anywhere|offsite|virtual)\b`)
)

// norm lowercases a name and reduces it to words: "King's Cross" and "Kings
// Cross" are one name, and so are "Stratford-upon-Avon" and "Stratford upon Avon".
func norm(s string) string {
	s = strings.ReplaceAll(strings.ToLower(s), "'", "")
	return strings.TrimSpace(nonWord.ReplaceAllString(s, " "))
}

// Place reads a location string.
//
// It splits on the separators postings use between options (";", "|", " - ",
// " or ") and then on commas, and matches whole words, longest name first —
// so "New South Wales" is not Wales, "Northern Ireland" is not Ireland, and
// "Sutton Coldfield" is not Sutton. A UK place followed in the same option by a
// foreign state, province or country is that foreign place: "Birmingham, AL",
// "London, Ontario".
//
// UK-wide is a string that names no UK town at all — "United Kingdom",
// "Remote - UK" — or an option that is remote in the UK, as in "Edinburgh or
// Remote (UK)". A country beside a town only says which country the town is
// in: "Rugby, United Kingdom" is Rugby, and so is a town or office the
// gazetteer does not know, "Chippenham Area, United Kingdom" or Workday's
// "Welcome Building, United Kingdom". Those count as elsewhere in the UK.
//
// A string it cannot read at all places nowhere, and the keyword tiers decide.
func Place(loc string) Where {
	var w Where
	marker := false
	for _, seg := range segmentSep.Split(loc, -1) {
		parts := partSep.Split(seg, -1)
		var kinds []kind
		for _, p := range parts {
			kinds = append(kinds, partKind(p))
		}
		named, inUK, unknown := false, false, false
		for i, k := range kinds {
			unknown = unknown || k == none && strings.ContainsAny(norm(parts[i]), "abcdefghijklmnopqrstuvwxyz")
			if (k == london || k == westMids || k == ukPlace) && i+1 < len(kinds) && kinds[i+1] == qualifier {
				k = abroad // the UK name was a foreign town's
			}
			switch k {
			case london:
				w |= InLondon
			case westMids:
				w |= InWestMidlands
			case ukPlace:
				w |= InUK
			case ukMarker:
				marker, inUK = true, true
			case abroad, qualifier:
				w |= Abroad
			}
			named = named || k == london || k == westMids || k == ukPlace
		}
		switch {
		case (named || inUK) && remote.MatchString(norm(seg)):
			w |= UKWide
		case inUK && unknown && !named:
			w |= InUK // a town or office it does not know; beside one it knows, an address
		}
	}
	if marker && w&(InLondon|InWestMidlands|InUK) == 0 {
		w |= UKWide
	}
	return w
}

// partKind is what one comma-separated part names. Within a part a foreign
// name wins over a UK one — "London Ontario" is in Canada — and a country or
// state over a city, since only those can make the part before them foreign.
// Among UK places the first named wins: buildings borrow London's district
// names, and "Manchester Westminster House" is Lloyds in Manchester.
func partKind(part string) kind {
	p := norm(part)
	if p == "" {
		return none
	}
	if codes[p] {
		return qualifier
	}
	if k := postcodeKind(part); k != none {
		return k
	}
	// Every run of words, longest first, so a longer name claims its words
	// before a shorter one inside it can.
	words := strings.Fields(p)
	claimed := make([]bool, len(words))
	var uk, foreign kind
	ukAt := len(words) // where the UK place chosen so far starts
	for n := min(len(words), 5); n > 0; n-- {
		for i := 0; i+n <= len(words); i++ {
			if anyClaimed(claimed[i : i+n]) {
				continue
			}
			k, ok := places[strings.Join(words[i:i+n], " ")]
			if !ok {
				continue
			}
			for j := i; j < i+n; j++ {
				claimed[j] = true
			}
			switch {
			case k == qualifier:
				foreign = qualifier
			case k == abroad:
				if foreign == none {
					foreign = abroad
				}
			case k == ukMarker:
				if uk == none {
					uk = k
				}
			case uk == ukMarker || i < ukAt:
				uk, ukAt = k, i
			}
		}
	}
	if foreign != none {
		return foreign
	}
	return uk
}

func anyClaimed(b []bool) bool {
	for _, c := range b {
		if c {
			return true
		}
	}
	return false
}

// postcodeKind places a full UK postcode. The metropolitan county's districts
// are B1–48, B62–76, B90–94, CV1–7, WV1–14, WS1–10 and DY1–9; the rest of
// those areas reach into Warwickshire, Worcestershire and Staffordshire.
//
// ponytail: district-level, so a district split across the county line counts
// as inside it.
func postcodeKind(part string) kind {
	m := postcode.FindStringSubmatch(part)
	if m == nil {
		return none
	}
	area := strings.ToUpper(m[1])
	d, _ := strconv.Atoi(m[2])
	switch area {
	case "E", "EC", "N", "NW", "SE", "SW", "W", "WC":
		return london
	case "B":
		if d <= 48 || d >= 62 && d <= 76 || d >= 90 && d <= 94 {
			return westMids
		}
	case "CV":
		if d <= 7 {
			return westMids
		}
	case "WV":
		if d <= 14 {
			return westMids
		}
	case "WS":
		if d <= 10 {
			return westMids
		}
	case "DY":
		if d <= 9 {
			return westMids
		}
	}
	return ukPlace
}
