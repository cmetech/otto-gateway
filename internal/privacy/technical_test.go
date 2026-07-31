package privacy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTechnical_IPv4StandaloneUsesStableSource24Relation(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	lease := newTechnicalTestLease(t, "ipv4-standalone", 32)
	defer lease.Release()

	first := mapTechnical(t, mapper, lease, "IPv4", "10.20.30.7", ProvenanceInput)
	second := mapTechnical(t, mapper, lease, "IPv4", "10.20.30.99", ProvenanceInput)
	third := mapTechnical(t, mapper, lease, "IPv4", "10.20.31.7", ProvenanceInput)

	benchmark := netip.MustParsePrefix("198.18.0.0/15")
	firstAddr := netip.MustParseAddr(first)
	secondAddr := netip.MustParseAddr(second)
	thirdAddr := netip.MustParseAddr(third)
	for _, addr := range []netip.Addr{firstAddr, secondAddr, thirdAddr} {
		if !benchmark.Contains(addr) {
			t.Fatalf("IPv4 alias %s is outside %s", addr, benchmark)
		}
	}
	if firstAddr.As4()[3] != 7 || secondAddr.As4()[3] != 99 || thirdAddr.As4()[3] != 7 {
		t.Fatalf("host octets were not preserved: %s, %s, %s", first, second, third)
	}
	if netip.PrefixFrom(firstAddr, 24).Masked() != netip.PrefixFrom(secondAddr, 24).Masked() {
		t.Fatalf("same source /24 mapped to different aliases: %s and %s", first, second)
	}
	if netip.PrefixFrom(firstAddr, 24).Masked() == netip.PrefixFrom(thirdAddr, 24).Masked() {
		t.Fatalf("distinct source /24s mapped to the same alias: %s and %s", first, third)
	}
}

func TestTechnical_IPv4CIDRPreservesAllowedPrefixAndHostOffset(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	lease := newTechnicalTestLease(t, "ipv4-cidr", 32)
	defer lease.Release()

	for _, original := range []string{
		"10.21.30.77/24",
		"10.22.31.129/25",
		"10.24.0.1/32",
	} {
		t.Run(original, func(t *testing.T) {
			alias := mapTechnical(t, mapper, lease, "IPv4", original, ProvenanceInput)
			assertPrefixAndOffset(t, original, alias, netip.MustParsePrefix("198.18.0.0/15"))
		})
	}

	if _, err := mapper.Map(lease, "IPv4", "10.20.30.40/14", ProvenanceInput); err == nil {
		t.Fatal("IPv4 /14 succeeded, want an error for a prefix broader than /15")
	}
}

func TestTechnical_IPv4CIDRReturnsTypedCapacityWithoutWeakeningPrefix(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	lease := newTechnicalTestLease(t, "ipv4-capacity", 8)
	defer lease.Release()

	first := mapTechnical(t, mapper, lease, "IPv4", "10.0.0.1/15", ProvenanceInput)
	assertPrefixAndOffset(t, "10.0.0.1/15", first, netip.MustParsePrefix("198.18.0.0/15"))

	_, err := mapper.Map(lease, "IPv4", "12.0.0.1/15", ProvenanceInput)
	var capacityErr *TechnicalCapacityError
	if !errors.As(err, &capacityErr) {
		t.Fatalf("second IPv4 /15 error = %v, want *TechnicalCapacityError", err)
	}
}

func TestTechnical_IPv4CIDRExhaustivelyProbesFree16Relation(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	lease := newTechnicalTestLease(t, "false-exhaustion", 8)
	defer lease.Release()

	firstOriginal := "1.0.0.17/16"
	secondOriginal := "12.0.0.203/16"
	firstAlias := mapTechnical(t, mapper, lease, "IPv4", firstOriginal, ProvenanceInput)
	secondAlias := mapTechnical(t, mapper, lease, "IPv4", secondOriginal, ProvenanceInput)

	firstPrefix := netip.MustParsePrefix(firstAlias)
	secondPrefix := netip.MustParsePrefix(secondAlias)
	if firstPrefix.Masked() == secondPrefix.Masked() {
		t.Fatalf("distinct /16 relations reused %s", firstPrefix.Masked())
	}
	benchmark := netip.MustParsePrefix("198.18.0.0/15")
	assertPrefixAndOffset(t, firstOriginal, firstAlias, benchmark)
	assertPrefixAndOffset(t, secondOriginal, secondAlias, benchmark)
}

func TestTechnical_IPv4CIDRConcurrentAllocationUsesBoth16Relations(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	lease := newTechnicalTestLease(t, "false-exhaustion", 8)
	defer lease.Release()

	originals := []string{"1.0.0.17/16", "12.0.0.203/16"}
	type result struct {
		original string
		alias    string
		err      error
	}
	results := make(chan result, len(originals))
	var group sync.WaitGroup
	for _, original := range originals {
		group.Add(1)
		go func() {
			defer group.Done()
			alias, err := mapper.Map(lease, "IPv4", original, ProvenanceInput)
			results <- result{original: original, alias: alias, err: err}
		}()
	}
	group.Wait()
	close(results)

	relations := make(map[netip.Prefix]struct{}, len(originals))
	for mapped := range results {
		if mapped.err != nil {
			t.Fatalf("Map(%s): %v", mapped.original, mapped.err)
		}
		assertPrefixAndOffset(t, mapped.original, mapped.alias, netip.MustParsePrefix("198.18.0.0/15"))
		relations[netip.MustParsePrefix(mapped.alias).Masked()] = struct{}{}
	}
	if len(relations) != 2 {
		t.Fatalf("concurrent /16 relation count = %d, want 2", len(relations))
	}
}

func TestTechnical_IPv6StandaloneUsesStableSource64Relation(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	lease := newTechnicalTestLease(t, "ipv6-standalone", 32)
	defer lease.Release()

	first := mapTechnical(t, mapper, lease, "IPv6", "2001:4860:1234:5678::abcd", ProvenanceInput)
	second := mapTechnical(t, mapper, lease, "IPv6", "2001:4860:1234:5678::dcba", ProvenanceInput)
	third := mapTechnical(t, mapper, lease, "IPv6", "2001:4860:1234:5679::abcd", ProvenanceInput)

	documentation := netip.MustParsePrefix("2001:db8::/32")
	firstAddr := netip.MustParseAddr(first)
	secondAddr := netip.MustParseAddr(second)
	thirdAddr := netip.MustParseAddr(third)
	for _, addr := range []netip.Addr{firstAddr, secondAddr, thirdAddr} {
		if !documentation.Contains(addr) {
			t.Fatalf("IPv6 alias %s is outside %s", addr, documentation)
		}
	}
	if netip.PrefixFrom(firstAddr, 64).Masked() != netip.PrefixFrom(secondAddr, 64).Masked() {
		t.Fatalf("same source /64 mapped to different aliases: %s and %s", first, second)
	}
	if netip.PrefixFrom(firstAddr, 64).Masked() == netip.PrefixFrom(thirdAddr, 64).Masked() {
		t.Fatalf("distinct source /64s mapped to the same alias: %s and %s", first, third)
	}
	firstBytes := firstAddr.As16()
	originalBytes := netip.MustParseAddr("2001:4860:1234:5678::abcd").As16()
	if got, want := firstBytes[8:], originalBytes[8:]; string(got) != string(want) {
		t.Fatalf("IPv6 host offset = %x, want %x", got, want)
	}
}

func TestTechnical_IPv6CIDRPreservesAllowedPrefixAndHostOffset(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	lease := newTechnicalTestLease(t, "ipv6-cidr", 32)
	defer lease.Release()

	for _, original := range []string{
		"2001:4860:1234:5678:9abc:def0:1234:5678/64",
		"2001:4860:1234:5678:9abc:def0:1234:5678/80",
		"2001:4860:1234:5678:9abc:def0:1234:5678/128",
	} {
		t.Run(original, func(t *testing.T) {
			alias := mapTechnical(t, mapper, lease, "IPv6", original, ProvenanceInput)
			assertPrefixAndOffset(t, original, alias, netip.MustParsePrefix("2001:db8::/32"))
		})
	}

	if _, err := mapper.Map(lease, "IPv6", "2001:4860::1/31", ProvenanceInput); err == nil {
		t.Fatal("IPv6 /31 succeeded, want an error for a prefix broader than /32")
	}
}

func TestTechnical_IPv6CIDR33CandidateSequenceCoversBothSlots(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	base := netip.MustParsePrefix("2001:db8::/32")
	for _, relation := range []string{
		"2001:4860::/33",
		"2001:4860:8000::/33",
		"2606:4700::/33",
		"2606:4700:8000::/33",
	} {
		t.Run(relation, func(t *testing.T) {
			candidates := make(map[netip.Prefix]struct{}, 2)
			for attempt := uint32(0); attempt < 2; attempt++ {
				candidate, err := mapper.ipRelationCandidate(
					"ipv6-permutation",
					"IPv6",
					relation,
					base,
					33,
					attempt,
				)
				if err != nil {
					t.Fatal(err)
				}
				candidates[candidate] = struct{}{}
			}
			if len(candidates) != 2 {
				t.Fatalf("/33 candidate sequence covered %d slots, want 2", len(candidates))
			}

			_, err := mapper.ipRelationCandidate("ipv6-permutation", "IPv6", relation, base, 33, 2)
			var capacityErr *TechnicalCapacityError
			if !errors.As(err, &capacityErr) {
				t.Fatalf("attempt after both /33 slots returned %v, want *TechnicalCapacityError", err)
			}
		})
	}
}

func TestTechnical_IPv6CIDRWideSelectorCoversReachableAttemptBounds(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	base := netip.MustParsePrefix("2001:db8::/32")
	attempts := []uint32{0, 1, ^uint32(0) - 1, ^uint32(0)}
	candidates := make(map[netip.Prefix]struct{}, len(attempts))
	for _, attempt := range attempts {
		candidate, err := mapper.ipRelationCandidate(
			"ipv6-wide-permutation",
			"IPv6",
			"2001:4860:1234:5678:9abc:def0:1234:5678/128",
			base,
			128,
			attempt,
		)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if !base.Contains(candidate.Addr()) || candidate.Bits() != 128 {
			t.Fatalf("attempt %d candidate = %s, want IPv6 documentation /128", attempt, candidate)
		}
		candidates[candidate] = struct{}{}
	}
	if len(candidates) != len(attempts) {
		t.Fatalf("wide selector covered %d unique candidates for %d reachable attempts", len(candidates), len(attempts))
	}
}

func TestTechnical_FormatsPreserveMACShapeAndAddressBits(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	lease := newTechnicalTestLease(t, "formats-mac", 16)
	defer lease.Release()

	for _, tc := range []struct {
		original  string
		separator string
		wantUpper bool
	}{
		{original: "00:1B:44:11:3A:B7", separator: ":", wantUpper: true},
		{original: "aa-bb-cc-dd-ee-ff", separator: "-", wantUpper: false},
	} {
		t.Run(tc.original, func(t *testing.T) {
			alias := mapTechnical(t, mapper, lease, "MAC_ADDRESS", tc.original, ProvenanceInput)
			parts := strings.Split(alias, tc.separator)
			if len(parts) != 6 {
				t.Fatalf("MAC alias = %q, want six %q-separated octets", alias, tc.separator)
			}
			first, err := strconv.ParseUint(parts[0], 16, 8)
			if err != nil {
				t.Fatalf("MAC first octet: %v", err)
			}
			if first&0x02 == 0 || first&0x01 != 0 {
				t.Fatalf("MAC first octet = %#02x, want local bit 1 and multicast bit 0", first)
			}
			if tc.wantUpper && alias != strings.ToUpper(alias) {
				t.Fatalf("MAC alias = %q, want uppercase hex", alias)
			}
			if !tc.wantUpper && alias != strings.ToLower(alias) {
				t.Fatalf("MAC alias = %q, want lowercase hex", alias)
			}
		})
	}
}

func TestTechnical_FormatsPreserveTelecomDigitContracts(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	lease := newTechnicalTestLease(t, "formats-digits", 16)
	defer lease.Release()

	imei := mapTechnical(t, mapper, lease, "IMEI", "490154203237518", ProvenanceInput)
	if len(imei) != 15 || !allDecimalDigits(imei) || !validTestLuhn(imei) {
		t.Fatalf("IMEI alias = %q, want 15 Luhn-valid digits", imei)
	}
	imsi := mapTechnical(t, mapper, lease, "IMSI", "310150123456789", ProvenanceInput)
	if len(imsi) != 15 || !allDecimalDigits(imsi) {
		t.Fatalf("IMSI alias = %q, want 15 digits", imsi)
	}
	msisdn := mapTechnical(t, mapper, lease, "MSISDN", "+442071838750", ProvenanceInput)
	if len(msisdn) != len("+442071838750") || !strings.HasPrefix(msisdn, "+") ||
		!allDecimalDigits(msisdn[1:]) || msisdn[1] == '0' {
		t.Fatalf("MSISDN alias = %q, want plus, preserved digit count, and non-zero first digit", msisdn)
	}
}

func TestTechnical_FormatsPreserveSIPSchemeAndOptionalPort(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	lease := newTechnicalTestLease(t, "formats-sip", 16)
	defer lease.Release()

	withoutPort := mapTechnical(t, mapper, lease, "SIP_URI", "sip:alice@atlanta.example.com", ProvenanceInput)
	if !regexp.MustCompile(`^sip:u-[a-z2-7]+@gw\.invalid$`).MatchString(withoutPort) {
		t.Fatalf("SIP alias = %q, want sip:u-<base32>@gw.invalid", withoutPort)
	}
	withPort := mapTechnical(t, mapper, lease, "SIP_URI", "sips:bob@biloxi.example.com:5061", ProvenanceInput)
	match := regexp.MustCompile(`^sips:u-[a-z2-7]+@gw\.invalid:([0-9]+)$`).FindStringSubmatch(withPort)
	if match == nil {
		t.Fatalf("SIPS alias = %q, want sips:u-<base32>@gw.invalid:<port>", withPort)
	}
	port, err := strconv.Atoi(match[1])
	if err != nil || port < 49152 || port > 65535 {
		t.Fatalf("SIPS alias port = %q, want 49152..65535", match[1])
	}
}

func TestTechnical_FormatsPreserveRecognizedSITEType(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	lease := newTechnicalTestLease(t, "formats-site", 16)
	defer lease.Release()

	for _, tc := range []struct {
		original string
		prefix   string
	}{
		{original: "site-A12_NYC01", prefix: "SITE"},
		{original: "site-A_B", prefix: "SITE"},
		{original: "ENB-12345", prefix: "ENB"},
		{original: "BTS_AB12", prefix: "BTS"},
	} {
		t.Run(tc.original, func(t *testing.T) {
			alias := mapTechnical(t, mapper, lease, "SITE", tc.original, ProvenanceInput)
			want := regexp.MustCompile(`^` + tc.prefix + `-SYN-[A-Z2-7]{10}$`)
			if !want.MatchString(alias) {
				t.Fatalf("SITE alias = %q, want %s-SYN-<10 base32 chars>", alias, tc.prefix)
			}
		})
	}
}

func TestTechnical_FormatsCoordinatesUseOneDistancePreservingScopeRotation(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	lease := newTechnicalTestLease(t, "formats-coordinates", 16)
	defer lease.Release()

	originalA := "37.774900°N, 122.419400°W"
	originalB := "34.052200°N, 118.243700°W"
	aliasA := mapTechnical(t, mapper, lease, "COORDINATES", originalA, ProvenanceInput)
	aliasB := mapTechnical(t, mapper, lease, "COORDINATES", originalB, ProvenanceInput)

	coordinateShape := regexp.MustCompile(`^\d{1,2}\.\d{6}°[NS], \d{1,3}\.\d{6}°[EW]$`)
	for _, alias := range []string{aliasA, aliasB} {
		if !coordinateShape.MatchString(alias) {
			t.Fatalf("coordinate alias = %q, want preserved precision, degree sign, separator, and hemispheres", alias)
		}
		coordinate := parseTestCoordinates(t, alias)
		if math.Abs(coordinate.lat) > math.Pi/2 || math.Abs(coordinate.lon) > math.Pi {
			t.Fatalf("coordinate alias = %q is outside valid latitude/longitude", alias)
		}
	}

	originalDistance := testGreatCircleDistance(parseTestCoordinates(t, originalA), parseTestCoordinates(t, originalB))
	aliasDistance := testGreatCircleDistance(parseTestCoordinates(t, aliasA), parseTestCoordinates(t, aliasB))
	if delta := math.Abs(aliasDistance - originalDistance); delta > 1e-6 {
		t.Fatalf("great-circle distance changed by %.12f radians, want <= 0.000001", delta)
	}
}

func TestTechnical_CoordinateFormattedDistanceAcrossAcceptedPrecisionAndEdges(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	cases := []struct {
		name   string
		scope  string
		first  string
		second string
	}{
		{
			name:   "one decimal",
			scope:  "formats-coordinates",
			first:  "37.7°N, 122.4°W",
			second: "34.0°N, 118.2°W",
		},
		{
			name:   "two decimals near north pole",
			scope:  "coordinate-near-pole",
			first:  "89.91°N, 10.25°E",
			second: "89.87°N, 170.75°W",
		},
		{
			name:   "three decimals across antimeridian",
			scope:  "coordinate-antimeridian",
			first:  "10.125°N, 179.875°E",
			second: "10.625°S, 179.625°W",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lease := newTechnicalTestLease(t, tc.scope, 8)
			defer lease.Release()

			assertTechnicalCoordinateDistance(t, mapper, lease, tc.first, tc.second)
		})
	}
}

func TestTechnical_CoordinateFormattedDistanceAcrossDeterministicScopeTable(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	for scopeIndex := range 32 {
		scopeID := fmt.Sprintf("coordinate-property-%02d", scopeIndex)
		t.Run(scopeID, func(t *testing.T) {
			lease := newTechnicalTestLease(t, scopeID, 8)
			defer lease.Release()

			assertTechnicalCoordinateDistance(
				t,
				mapper,
				lease,
				"12.3°S, 179.9°E",
				"87.6°N, 179.8°W",
			)
		})
	}
}

func TestTechnical_CoordinateScopeStabilityAndUnlinkability(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	firstScope := newTechnicalTestLease(t, "coordinate-stability-a", 8)
	defer firstScope.Release()
	secondScope := newTechnicalTestLease(t, "coordinate-stability-b", 8)
	defer secondScope.Release()

	original := "0.0°N, 179.9°E"
	first := mapTechnical(t, mapper, firstScope, "COORDINATES", original, ProvenanceInput)
	repeated := mapTechnical(t, mapper, firstScope, "COORDINATES", original, ProvenanceInput)
	second := mapTechnical(t, mapper, secondScope, "COORDINATES", original, ProvenanceInput)
	if repeated != first {
		t.Fatalf("same-scope coordinate alias = %q, want stable %q", repeated, first)
	}
	if second == first {
		t.Fatalf("cross-scope coordinate alias = %q in both scopes", first)
	}
}

func TestTechnical_CoordinateConcreteRetainedScopeCollision(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	store := newCoordinateTestStore(t, 2)
	firstLease := acquireTestScope(t, store, "coordinate-many-scope-000")
	defer firstLease.Release()
	secondLease := acquireTestScope(t, store, "coordinate-many-scope-005")
	defer secondLease.Release()

	original := "37.7°N, 122.4°W"
	first := mapTechnical(t, mapper, firstLease, "COORDINATES", original, ProvenanceInput)
	second := mapTechnical(t, mapper, secondLease, "COORDINATES", original, ProvenanceInput)
	if first == second {
		t.Fatalf("distinct retained scopes reused coordinate alias %q", first)
	}
}

func TestTechnical_CoordinateRetainedDefaultScopeUniqueness(t *testing.T) {
	const retainedScopes = 128

	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	store := newCoordinateTestStore(t, retainedScopes)
	aliases := make(map[string]string, retainedScopes)
	leases := make([]*ScopeLease, 0, retainedScopes)
	defer func() {
		for _, lease := range leases {
			lease.Release()
		}
	}()

	for index := range retainedScopes {
		scopeID := fmt.Sprintf("coordinate-many-scope-%03d", index)
		lease := acquireTestScope(t, store, scopeID)
		leases = append(leases, lease)
		original := "37.7°N, 122.4°W"
		alias := mapTechnical(t, mapper, lease, "COORDINATES", original, ProvenanceInput)
		if priorScope, exists := aliases[alias]; exists {
			t.Fatalf("scopes %q and %q reused coordinate alias %q", priorScope, scopeID, alias)
		}
		aliases[alias] = scopeID
		if repeated := mapTechnical(t, mapper, lease, "COORDINATES", original, ProvenanceInput); repeated != alias {
			t.Fatalf("scope %q repeat = %q, want %q", scopeID, repeated, alias)
		}
		assertTechnicalCoordinateDistance(
			t,
			mapper,
			lease,
			original,
			"34.0°N, 118.2°W",
		)
	}
}

func TestTechnical_CoordinateConcurrentRetainedScopeAllocation(t *testing.T) {
	const retainedScopes = 128

	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	store := newCoordinateTestStore(t, retainedScopes)
	leases := make([]*ScopeLease, 0, retainedScopes)
	for index := range retainedScopes {
		leases = append(leases, acquireTestScope(t, store, fmt.Sprintf("coordinate-concurrent-%03d", index)))
	}
	defer func() {
		for _, lease := range leases {
			lease.Release()
		}
	}()

	type result struct {
		scopeID string
		alias   string
		err     error
	}
	results := make(chan result, retainedScopes)
	var group sync.WaitGroup
	for _, lease := range leases {
		group.Add(1)
		go func() {
			defer group.Done()
			alias, err := mapper.Map(lease, "COORDINATES", "12.3°S, 179.9°E", ProvenanceInput)
			results <- result{scopeID: lease.scope.id, alias: alias, err: err}
		}()
	}
	group.Wait()
	close(results)

	aliases := make(map[string]string, retainedScopes)
	for mapped := range results {
		if mapped.err != nil {
			t.Fatalf("scope %q: %v", mapped.scopeID, mapped.err)
		}
		if priorScope, exists := aliases[mapped.alias]; exists {
			t.Fatalf("concurrent scopes %q and %q reused alias %q", priorScope, mapped.scopeID, mapped.alias)
		}
		aliases[mapped.alias] = mapped.scopeID
	}
}

func TestTechnical_CoordinateRegistryPrunesClearedAndExpiredScopes(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	clock := newFakeClock()
	store := newTestScopeStore(t, clock, StoreConfig{
		TTL:                time.Minute,
		MaxScopes:          4,
		MaxEntriesPerScope: 2,
		MaxTotalEntries:    8,
	})

	cleared := acquireTestScope(t, store, "coordinate-cleared")
	mapTechnical(t, mapper, cleared, "COORDINATES", "37.7°N, 122.4°W", ProvenanceInput)
	cleared.Release()
	if result, err := store.Clear("coordinate-cleared"); err != nil || result != ClearCompleted {
		t.Fatalf("Clear(coordinate-cleared) = (%q, %v)", result, err)
	}

	expired := acquireTestScope(t, store, "coordinate-expired")
	mapTechnical(t, mapper, expired, "COORDINATES", "37.7°N, 122.4°W", ProvenanceInput)
	expired.Release()
	clock.Advance(2 * time.Minute)
	if reaped := store.ReapExpired(); reaped != 1 {
		t.Fatalf("ReapExpired() = %d, want 1", reaped)
	}

	retained := acquireTestScope(t, store, "coordinate-retained")
	defer retained.Release()
	mapTechnical(t, mapper, retained, "COORDINATES", "37.7°N, 122.4°W", ProvenanceInput)

	mapper.coordinateMu.Lock()
	registry := mapper.coordinateRegistries[store]
	if registry == nil {
		mapper.coordinateMu.Unlock()
		t.Fatal("coordinate registry missing after allocation")
	}
	if len(registry.byScope) != 1 || len(registry.byRotation) != 1 {
		mapper.coordinateMu.Unlock()
		t.Fatalf("registry sizes = (%d scopes, %d rotations), want (1, 1)", len(registry.byScope), len(registry.byRotation))
	}
	_, hasCleared := registry.byScope["coordinate-cleared"]
	_, hasExpired := registry.byScope["coordinate-expired"]
	_, hasRetained := registry.byScope["coordinate-retained"]
	mapper.coordinateMu.Unlock()
	if hasCleared || hasExpired || !hasRetained {
		t.Fatalf("registry IDs: cleared=%t expired=%t retained=%t", hasCleared, hasExpired, hasRetained)
	}
}

func TestTechnical_CoordinateSymmetryExhaustionIsTypedAndFailClosed(t *testing.T) {
	const totalScopes = coordinateSymmetryCount + 1

	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	store := newTestScopeStore(t, newFakeClock(), StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          totalScopes,
		MaxEntriesPerScope: 1,
		MaxTotalEntries:    totalScopes,
	})
	for index := 0; index < coordinateSymmetryCount; index++ {
		lease := acquireTestScope(t, store, fmt.Sprintf("coordinate-capacity-%04d", index))
		lease.Release()
	}
	target := acquireTestScope(t, store, "coordinate-capacity-target")
	defer target.Release()

	registry := &coordinateRotationRegistry{
		byScope:    make(map[string]int, coordinateSymmetryCount),
		byRotation: make(map[int]string, coordinateSymmetryCount),
	}
	for rotationIndex := 0; rotationIndex < coordinateSymmetryCount; rotationIndex++ {
		scopeID := fmt.Sprintf("coordinate-capacity-%04d", rotationIndex)
		registry.byScope[scopeID] = rotationIndex
		registry.byRotation[rotationIndex] = scopeID
	}
	mapper.coordinateMu.Lock()
	mapper.coordinateRegistries[store] = registry
	mapper.coordinateMu.Unlock()

	alias, err := mapper.Map(target, "COORDINATES", "37.7°N, 122.4°W", ProvenanceInput)
	var capacityErr *TechnicalCapacityError
	if alias != "" || !errors.As(err, &capacityErr) {
		t.Fatalf("exhausted Map() = (%q, %v), want empty alias and *TechnicalCapacityError", alias, err)
	}
	if entries := mustInspect(t, store, "coordinate-capacity-target"); len(entries) != 0 {
		t.Fatalf("exhausted target stored %d mappings, want 0", len(entries))
	}
}

func newCoordinateTestStore(t *testing.T, maxScopes int) *ScopeStore {
	t.Helper()

	return newTestScopeStore(t, newFakeClock(), StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          maxScopes,
		MaxEntriesPerScope: 4,
		MaxTotalEntries:    maxScopes * 4,
	})
}

func assertTechnicalCoordinateDistance(
	t *testing.T,
	mapper *TechnicalMapper,
	lease *ScopeLease,
	first string,
	second string,
) {
	t.Helper()

	firstAlias := mapTechnical(t, mapper, lease, "COORDINATES", first, ProvenanceInput)
	secondAlias := mapTechnical(t, mapper, lease, "COORDINATES", second, ProvenanceInput)
	originalDistance := testGreatCircleDistance(parseTestCoordinates(t, first), parseTestCoordinates(t, second))
	formattedAliasDistance := testGreatCircleDistance(
		parseTestCoordinates(t, firstAlias),
		parseTestCoordinates(t, secondAlias),
	)
	if delta := math.Abs(formattedAliasDistance - originalDistance); delta > 1e-6 {
		t.Fatalf(
			"formatted aliases %q and %q changed great-circle distance by %.12f radians",
			firstAlias,
			secondAlias,
			delta,
		)
	}
}

func TestTechnical_StabilityAndCrossScopeUnlinkability(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	store := newTestScopeStore(t, newFakeClock(), StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          4,
		MaxEntriesPerScope: 16,
		MaxTotalEntries:    64,
	})
	leaseA := acquireTestScope(t, store, "stable-a")
	defer leaseA.Release()
	leaseB := acquireTestScope(t, store, "stable-b")
	defer leaseB.Release()

	first := mapTechnical(t, mapper, leaseA, "SIP_URI", "sips:alice@example.com:5061", ProvenanceInput)
	repeated := mapTechnical(t, mapper, leaseA, "SIP_URI", "sips:alice@example.com:5061", ProvenanceInput)
	crossScope := mapTechnical(t, mapper, leaseB, "SIP_URI", "sips:alice@example.com:5061", ProvenanceInput)
	if repeated != first {
		t.Fatalf("same-scope repeat = %q, want %q", repeated, first)
	}
	if crossScope == first {
		t.Fatalf("cross-scope alias = %q, want unlinkable from scope A", crossScope)
	}
}

func TestTechnical_HMACUsesCopiedKeyAndLengthPrefixedDomainFields(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	mapper := newTestTechnicalMapper(t, key)
	digest := mapper.derive("scope", "IPv4", "10.0.0.0/24", 7)

	mac := hmac.New(sha256.New, []byte("0123456789abcdef0123456789abcdef"))
	for _, field := range [][]byte{
		[]byte(technicalHMACDomain),
		[]byte("scope"),
		[]byte("IPv4"),
		[]byte("10.0.0.0/24"),
		{0, 0, 0, 7},
	} {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(field)))
		_, _ = mac.Write(length[:])
		_, _ = mac.Write(field)
	}
	var want [sha256.Size]byte
	copy(want[:], mac.Sum(nil))
	if digest != want {
		t.Fatalf("HMAC digest = %x, want independently framed %x", digest, want)
	}
	if mapper.derive("ab", "c", "d", 0) == mapper.derive("a", "bc", "d", 0) {
		t.Fatal("length-prefixed tuples aliased across scope/entity boundaries")
	}

	for index := range key {
		key[index] ^= 0xff
	}
	if got := mapper.derive("scope", "IPv4", "10.0.0.0/24", 7); got != digest {
		t.Fatal("mapper retained caller-owned alias key storage")
	}
	if _, err := NewTechnicalMapper(nil); err == nil {
		t.Fatal("NewTechnicalMapper(nil) succeeded, want non-empty-key error")
	}
}

func TestTechnical_CollisionRetryPreservesIPv4Relation(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	lease := newTechnicalTestLease(t, "collision-retry", 16)
	defer lease.Release()

	base := netip.MustParsePrefix("198.18.0.0/15")
	type sourcePair struct {
		first  string
		second string
	}
	seen := make(map[string]string)
	var collision sourcePair
	for index := 0; index < 513; index++ {
		relation := fmt.Sprintf("10.%d.%d.0/24", index/256, index%256)
		candidate, err := mapper.ipRelationCandidate(lease.scope.id, "IPv4", relation, base, 24, 0)
		if err != nil {
			t.Fatal(err)
		}
		if prior, ok := seen[candidate.String()]; ok {
			retry, retryErr := mapper.ipRelationCandidate(lease.scope.id, "IPv4", relation, base, 24, 1)
			if retryErr != nil {
				t.Fatal(retryErr)
			}
			if retry.String() != candidate.String() {
				collision = sourcePair{first: prior, second: relation}
				break
			}
		}
		seen[candidate.String()] = relation
	}
	if collision == (sourcePair{}) {
		t.Fatal("failed to find deterministic first-attempt relation collision")
	}

	firstOriginal := strings.TrimSuffix(collision.first, "0/24") + "7"
	secondOriginal := strings.TrimSuffix(collision.second, "0/24") + "7"
	firstAlias := mapTechnical(t, mapper, lease, "IPv4", firstOriginal, ProvenanceInput)
	secondAlias := mapTechnical(t, mapper, lease, "IPv4", secondOriginal, ProvenanceInput)
	if netip.PrefixFrom(netip.MustParseAddr(firstAlias), 24).Masked() ==
		netip.PrefixFrom(netip.MustParseAddr(secondAlias), 24).Masked() {
		t.Fatalf("collision retry reused alias relation: %s and %s", firstAlias, secondAlias)
	}
	if netip.MustParseAddr(secondAlias).As4()[3] != 7 {
		t.Fatalf("collision retry weakened host-offset preservation: %s", secondAlias)
	}
}

func TestTechnical_ProvenanceControlsRestorationEligibilityAndReverseLookup(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	lease := newTechnicalTestLease(t, "provenance", 16)
	defer lease.Release()

	inputAlias := mapTechnical(t, mapper, lease, "MAC_ADDRESS", "00:1B:44:11:3A:B7", ProvenanceInput)
	generatedAlias := mapTechnical(t, mapper, lease, "MAC_ADDRESS", "00:1B:44:11:3A:B8", ProvenanceGenerated)

	inputEntry, ok := lease.ResolveSynthetic("MAC_ADDRESS", inputAlias)
	if !ok || inputEntry.Original != "00:1B:44:11:3A:B7" || inputEntry.Provenance != ProvenanceInput {
		t.Fatalf("input reverse lookup = (%+v, %t)", inputEntry, ok)
	}
	generatedEntry, ok := lease.ResolveSynthetic("MAC_ADDRESS", generatedAlias)
	if !ok || generatedEntry.Original != "00:1B:44:11:3A:B8" || generatedEntry.Provenance != ProvenanceGenerated {
		t.Fatalf("generated reverse lookup = (%+v, %t)", generatedEntry, ok)
	}
	if inputEntry.Provenance != ProvenanceInput {
		t.Fatal("caller input is not restoration-eligible")
	}
	if generatedEntry.Provenance == ProvenanceInput {
		t.Fatal("generated entry was treated as caller-input restoration")
	}
}

func TestTechnical_ProvenancePromotesGeneratedToInputAndNeverDowngrades(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))

	generatedFirst := newTechnicalTestLease(t, "provenance-generated-first", 8)
	defer generatedFirst.Release()
	generatedAlias := mapTechnical(t, mapper, generatedFirst, "IMSI", "310150123456789", ProvenanceGenerated)
	inputRepeat := mapTechnical(t, mapper, generatedFirst, "IMSI", "310150123456789", ProvenanceInput)
	if inputRepeat != generatedAlias {
		t.Fatalf("same value changed alias across provenance: %q and %q", generatedAlias, inputRepeat)
	}
	generatedEntry, ok := generatedFirst.ResolveSynthetic("IMSI", generatedAlias)
	if !ok || generatedEntry.Provenance != ProvenanceInput {
		t.Fatalf("generated-then-input entry = (%+v, %t), want promoted input provenance", generatedEntry, ok)
	}

	inputFirst := newTechnicalTestLease(t, "provenance-input-first", 8)
	defer inputFirst.Release()
	inputAlias := mapTechnical(t, mapper, inputFirst, "IMSI", "310150123456789", ProvenanceInput)
	generatedRepeat := mapTechnical(t, mapper, inputFirst, "IMSI", "310150123456789", ProvenanceGenerated)
	if generatedRepeat != inputAlias {
		t.Fatalf("same value changed alias across provenance: %q and %q", inputAlias, generatedRepeat)
	}
	inputEntry, ok := inputFirst.ResolveSynthetic("IMSI", inputAlias)
	if !ok || inputEntry.Provenance != ProvenanceInput {
		t.Fatalf("input-first entry = (%+v, %t), want input and restoration-eligible", inputEntry, ok)
	}
}

func TestTechnical_ConcurrentRepeatIsStableUnderRace(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	lease := newTechnicalTestLease(t, "concurrent-stability", 16)
	defer lease.Release()

	const workers = 32
	aliases := make(chan string, workers)
	errorsCh := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			alias, err := mapper.Map(lease, "IMSI", "310150123456789", ProvenanceInput)
			aliases <- alias
			errorsCh <- err
		}()
	}
	group.Wait()
	close(aliases)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first string
	for alias := range aliases {
		if first == "" {
			first = alias
		}
		if alias != first {
			t.Fatalf("concurrent alias = %q, want %q", alias, first)
		}
	}
	if entries := lease.store.Snapshot().Entries; entries != 1 {
		t.Fatalf("concurrent stable mapping consumed %d entries, want 1", entries)
	}
}

func TestTechnical_InvalidIMEIChecksumIsRejected(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	lease := newTechnicalTestLease(t, "invalid-imei", 8)
	defer lease.Release()

	if _, err := mapper.Map(lease, "IMEI", "490154203237519", ProvenanceInput); err == nil {
		t.Fatal("Luhn-invalid IMEI succeeded")
	}
}

func TestTechnical_ErrorsDoNotExposeOriginals(t *testing.T) {
	mapper := newTestTechnicalMapper(t, []byte("0123456789abcdef0123456789abcdef"))
	lease := newTechnicalTestLease(t, "error-redaction", 8)
	defer lease.Release()

	original := "490154203237519"
	_, err := mapper.Map(lease, "IMEI", original, ProvenanceInput)
	if err == nil {
		t.Fatal("Luhn-invalid IMEI succeeded")
	}
	if strings.Contains(err.Error(), original) {
		t.Fatalf("validation error exposed original: %v", err)
	}
}

func allDecimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func validTestLuhn(value string) bool {
	sum := 0
	parity := len(value) % 2
	for index, character := range value {
		digit := int(character - '0')
		if index%2 == parity {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
	}
	return sum%10 == 0
}

type testCoordinate struct {
	lat float64
	lon float64
}

func parseTestCoordinates(t *testing.T, value string) testCoordinate {
	t.Helper()

	match := regexp.MustCompile(`^(\d{1,3}\.\d+)°?([NS]), (\d{1,3}\.\d+)°?([EW])$`).FindStringSubmatch(value)
	if match == nil {
		t.Fatalf("cannot parse test coordinate %q", value)
	}
	latitude, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		t.Fatal(err)
	}
	longitude, err := strconv.ParseFloat(match[3], 64)
	if err != nil {
		t.Fatal(err)
	}
	if match[2] == "S" {
		latitude = -latitude
	}
	if match[4] == "W" {
		longitude = -longitude
	}
	return testCoordinate{lat: latitude * math.Pi / 180, lon: longitude * math.Pi / 180}
}

func testGreatCircleDistance(a, b testCoordinate) float64 {
	deltaLatitude := b.lat - a.lat
	deltaLongitude := b.lon - a.lon
	haversine := math.Sin(deltaLatitude/2)*math.Sin(deltaLatitude/2) +
		math.Cos(a.lat)*math.Cos(b.lat)*math.Sin(deltaLongitude/2)*math.Sin(deltaLongitude/2)
	return 2 * math.Atan2(math.Sqrt(haversine), math.Sqrt(1-haversine))
}

func newTestTechnicalMapper(t *testing.T, key []byte) *TechnicalMapper {
	t.Helper()

	mapper, err := NewTechnicalMapper(key)
	if err != nil {
		t.Fatal(err)
	}
	return mapper
}

func newTechnicalTestLease(t *testing.T, scopeID string, entries int) *ScopeLease {
	t.Helper()

	store := newTestScopeStore(t, newFakeClock(), StoreConfig{
		TTL:                time.Hour,
		MaxScopes:          4,
		MaxEntriesPerScope: entries,
		MaxTotalEntries:    entries * 4,
	})
	return acquireTestScope(t, store, scopeID)
}

func mapTechnical(
	t *testing.T,
	mapper *TechnicalMapper,
	lease *ScopeLease,
	entity string,
	original string,
	provenance Provenance,
) string {
	t.Helper()

	alias, err := mapper.Map(lease, entity, original, provenance)
	if err != nil {
		t.Fatal(err)
	}
	return alias
}

func assertPrefixAndOffset(t *testing.T, original, alias string, target netip.Prefix) {
	t.Helper()

	originalPrefix := netip.MustParsePrefix(original)
	aliasPrefix := netip.MustParsePrefix(alias)
	if aliasPrefix.Bits() != originalPrefix.Bits() {
		t.Fatalf("alias prefix = /%d, want /%d", aliasPrefix.Bits(), originalPrefix.Bits())
	}
	if !target.Contains(aliasPrefix.Addr()) || !target.Contains(aliasPrefix.Masked().Addr()) {
		t.Fatalf("alias %s is outside %s", aliasPrefix, target)
	}
	if got, want := hostBits(aliasPrefix), hostBits(originalPrefix); string(got) != string(want) {
		t.Fatalf("alias host offset = %x, want %x", got, want)
	}
}

func hostBits(prefix netip.Prefix) []byte {
	bits := prefix.Bits()
	if prefix.Addr().Is4() {
		addr := prefix.Addr().As4()
		return maskedHostBytes(addr[:], bits)
	}
	addr := prefix.Addr().As16()
	return maskedHostBytes(addr[:], bits)
}

func maskedHostBytes(addr []byte, prefixBits int) []byte {
	host := append([]byte(nil), addr...)
	for bit := 0; bit < prefixBits; bit++ {
		host[bit/8] &^= 1 << (7 - uint(bit%8))
	}
	return host
}
