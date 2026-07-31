package privacy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

const technicalHMACDomain = "otto-gateway/privacy/technical/v1"

const coordinateSymmetryCount = 7199

var (
	technicalMACPattern  = regexp.MustCompile(`^(?:[0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$|^(?:[0-9A-Fa-f]{2}-){5}[0-9A-Fa-f]{2}$`)
	technicalSIPPattern  = regexp.MustCompile(`^(sip|sips):[a-zA-Z0-9_.+\-]+@[a-zA-Z0-9.\-]+(?::([0-9]+))?$`)
	technicalSitePattern = regexp.MustCompile(
		`^site[-_\s]?[A-Z0-9]{1,2}[A-Z0-9_\-]{1,12}$` +
			`|^(ENB|BTS|NB|CELL|NODE|RAN|BSC|RNC|MSC|HLR|MME|SGW|PGW)[-_]?[A-Z0-9]{2,12}$`,
	)
	technicalCoordinatePattern = regexp.MustCompile(`^(\d{1,3})(\.\d+)(\s*°?\s*)([NS])([,\s]+)(\d{1,3})(\.\d+)(\s*°?\s*)([EW])$`)
	technicalBase32            = base32.StdEncoding.WithPadding(base32.NoPadding)
)

var (
	errTechnicalAliasKey = errors.New("technical alias key must not be empty")
	errTechnicalEntity   = errors.New("unsupported technical entity")
	errTechnicalValue    = errors.New("invalid technical value")
)

// TechnicalCapacityError reports that no alias can preserve the requested
// technical relationship. Callers must not fall back to plaintext.
type TechnicalCapacityError struct {
	Entity     string
	PrefixBits int
}

func (e *TechnicalCapacityError) Error() string {
	return fmt.Sprintf("technical alias capacity exhausted for %s /%d", e.Entity, e.PrefixBits)
}

// TechnicalMapper creates deterministic, scope-local technical aliases.
type TechnicalMapper struct {
	aliasKey []byte
}

// NewTechnicalMapper copies the non-empty key used for candidate HMACs.
func NewTechnicalMapper(aliasKey []byte) (*TechnicalMapper, error) {
	if len(aliasKey) == 0 {
		return nil, errTechnicalAliasKey
	}
	return &TechnicalMapper{aliasKey: append([]byte(nil), aliasKey...)}, nil
}

// Map returns the stable reversible alias for one technical value.
func (m *TechnicalMapper) Map(
	lease *ScopeLease,
	entity string,
	original string,
	provenance Provenance,
) (string, error) {
	if m == nil || len(m.aliasKey) == 0 || lease == nil {
		return "", errTechnicalValue
	}
	if provenance != ProvenanceInput && provenance != ProvenanceGenerated {
		return "", errInvalidProvenance
	}

	switch entity {
	case "IPv4", "IPv6":
		return m.mapIP(lease, entity, original, provenance)
	case "MAC_ADDRESS":
		return m.mapMAC(lease, original, provenance)
	case "IMEI":
		return m.mapIMEI(lease, original, provenance)
	case "IMSI":
		return m.mapIMSI(lease, original, provenance)
	case "MSISDN":
		return m.mapMSISDN(lease, original, provenance)
	case "SIP_URI":
		return m.mapSIP(lease, original, provenance)
	case "SITE":
		return m.mapSite(lease, original, provenance)
	case "COORDINATES":
		return m.mapCoordinates(lease, original, provenance)
	default:
		return "", fmt.Errorf("%s: %w", entity, errTechnicalEntity)
	}
}

func (m *TechnicalMapper) mapMAC(lease *ScopeLease, original string, provenance Provenance) (string, error) {
	if !technicalMACPattern.MatchString(original) {
		return "", fmt.Errorf("MAC_ADDRESS: %w", errTechnicalValue)
	}
	separator := original[2]
	uppercase := original == strings.ToUpper(original)
	return m.mapDerived(lease, "MAC_ADDRESS", original, provenance, func(digest [sha256.Size]byte) string {
		bytes := digest[:6]
		bytes[0] = (bytes[0] | 0x02) & 0xfe
		format := "%02x%c%02x%c%02x%c%02x%c%02x%c%02x"
		if uppercase {
			format = "%02X%c%02X%c%02X%c%02X%c%02X%c%02X"
		}
		return fmt.Sprintf(
			format,
			bytes[0], separator, bytes[1], separator, bytes[2], separator,
			bytes[3], separator, bytes[4], separator, bytes[5],
		)
	})
}

func (m *TechnicalMapper) mapIMEI(lease *ScopeLease, original string, provenance Provenance) (string, error) {
	if len(original) != 15 || !decimalDigits(original) || !luhnValid(original) {
		return "", fmt.Errorf("IMEI: %w", errTechnicalValue)
	}
	return m.mapDerived(lease, "IMEI", original, provenance, func(digest [sha256.Size]byte) string {
		body := decimalFromDigest(digest, 14, false)
		return body + string(luhnCheckDigit(body))
	})
}

func (m *TechnicalMapper) mapIMSI(lease *ScopeLease, original string, provenance Provenance) (string, error) {
	if len(original) != 15 || !decimalDigits(original) {
		return "", fmt.Errorf("IMSI: %w", errTechnicalValue)
	}
	return m.mapDerived(lease, "IMSI", original, provenance, func(digest [sha256.Size]byte) string {
		return decimalFromDigest(digest, len(original), false)
	})
}

func (m *TechnicalMapper) mapMSISDN(lease *ScopeLease, original string, provenance Provenance) (string, error) {
	if len(original) < 9 || len(original) > 16 || original[0] != '+' ||
		!decimalDigits(original[1:]) || original[1] == '0' {
		return "", fmt.Errorf("MSISDN: %w", errTechnicalValue)
	}
	return m.mapDerived(lease, "MSISDN", original, provenance, func(digest [sha256.Size]byte) string {
		return "+" + decimalFromDigest(digest, len(original)-1, true)
	})
}

func (m *TechnicalMapper) mapSIP(lease *ScopeLease, original string, provenance Provenance) (string, error) {
	match := technicalSIPPattern.FindStringSubmatch(original)
	if match == nil {
		return "", fmt.Errorf("SIP_URI: %w", errTechnicalValue)
	}
	if match[2] != "" {
		port, err := strconv.ParseUint(match[2], 10, 16)
		if err != nil || port == 0 {
			return "", fmt.Errorf("SIP_URI port: %w", errTechnicalValue)
		}
	}

	scheme := match[1]
	hasPort := match[2] != ""
	return m.mapDerived(lease, "SIP_URI", original, provenance, func(digest [sha256.Size]byte) string {
		// RFC 2606 reserves .invalid for names that must never resolve.
		// https://www.rfc-editor.org/rfc/rfc2606
		user := strings.ToLower(technicalBase32.EncodeToString(digest[:10]))
		alias := scheme + ":u-" + user + "@gw.invalid"
		if hasPort {
			port := 49152 + int(binary.BigEndian.Uint16(digest[10:12]))%16384
			alias += ":" + strconv.Itoa(port)
		}
		return alias
	})
}

func (m *TechnicalMapper) mapSite(lease *ScopeLease, original string, provenance Provenance) (string, error) {
	match := technicalSitePattern.FindStringSubmatch(original)
	if match == nil {
		return "", fmt.Errorf("SITE: %w", errTechnicalValue)
	}
	typePrefix := match[1]
	if strings.HasPrefix(strings.ToLower(original), "site") {
		typePrefix = "SITE"
	}
	typePrefix = strings.ToUpper(typePrefix)
	return m.mapDerived(lease, "SITE", original, provenance, func(digest [sha256.Size]byte) string {
		code := technicalBase32.EncodeToString(digest[:7])[:10]
		return typePrefix + "-SYN-" + code
	})
}

func (m *TechnicalMapper) mapCoordinates(lease *ScopeLease, original string, provenance Provenance) (string, error) {
	coordinate, err := parseTechnicalCoordinate(original)
	if err != nil {
		return "", err
	}
	rotation, err := m.coordinateRotation(lease)
	if err != nil {
		return "", err
	}
	synthetic := rotateTechnicalCoordinate(coordinate, rotation)

	entry, _, err := lease.GetOrCreate("COORDINATES", original, provenance, func(attempt uint32) (string, error) {
		if attempt != 0 {
			return "", &TechnicalCapacityError{Entity: "COORDINATES"}
		}
		return synthetic, nil
	})
	if err != nil {
		return "", err
	}
	return entry.Synthetic, nil
}

func (m *TechnicalMapper) mapDerived(
	lease *ScopeLease,
	entity string,
	original string,
	provenance Provenance,
	format func([sha256.Size]byte) string,
) (string, error) {
	entry, _, err := lease.GetOrCreate(entity, original, provenance, func(attempt uint32) (string, error) {
		if attempt == ^uint32(0) {
			return "", &TechnicalCapacityError{Entity: entity}
		}
		return format(m.derive(lease.scope.id, entity, original, attempt)), nil
	})
	if err != nil {
		return "", err
	}
	return entry.Synthetic, nil
}

func decimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func decimalFromDigest(digest [sha256.Size]byte, length int, firstNonZero bool) string {
	digits := make([]byte, length)
	for index := range digits {
		digit := digest[index%len(digest)] % 10
		if index == 0 && firstNonZero {
			digit = 1 + digest[index]%9
		}
		digits[index] = '0' + digit
	}
	return string(digits)
}

func luhnCheckDigit(body string) byte {
	for digit := byte('0'); digit <= '9'; digit++ {
		if luhnValid(body + string(digit)) {
			return digit
		}
	}
	return '0'
}

func luhnValid(value string) bool {
	sum := 0
	parity := len(value) % 2
	for index := range value {
		digit := int(value[index] - '0')
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

type technicalCoordinate struct {
	latitude            float64
	longitude           float64
	latitudePrecision   int
	longitudePrecision  int
	latitudeDecoration  string
	separator           string
	longitudeDecoration string
}

type technicalRotation struct {
	axis  [3]float64
	angle float64
}

func parseTechnicalCoordinate(original string) (technicalCoordinate, error) {
	match := technicalCoordinatePattern.FindStringSubmatch(original)
	if match == nil {
		return technicalCoordinate{}, fmt.Errorf("COORDINATES: %w", errTechnicalValue)
	}
	latitude, err := strconv.ParseFloat(match[1]+match[2], 64)
	if err != nil || latitude > 90 {
		return technicalCoordinate{}, fmt.Errorf("COORDINATES latitude: %w", errTechnicalValue)
	}
	longitude, err := strconv.ParseFloat(match[6]+match[7], 64)
	if err != nil || longitude > 180 {
		return technicalCoordinate{}, fmt.Errorf("COORDINATES longitude: %w", errTechnicalValue)
	}
	if match[4] == "S" {
		latitude = -latitude
	}
	if match[9] == "W" {
		longitude = -longitude
	}
	return technicalCoordinate{
		latitude:            latitude * math.Pi / 180,
		longitude:           longitude * math.Pi / 180,
		latitudePrecision:   len(match[2]) - 1,
		longitudePrecision:  len(match[7]) - 1,
		latitudeDecoration:  match[3],
		separator:           match[5],
		longitudeDecoration: match[8],
	}, nil
}

func (m *TechnicalMapper) coordinateRotation(lease *ScopeLease) (technicalRotation, error) {
	rotationIndex, err := lease.store.coordinateRotationIndex(
		lease.scope.id,
		func(attempt uint32) (uint16, error) {
			if attempt >= coordinateSymmetryCount {
				return 0, &TechnicalCapacityError{Entity: "COORDINATES"}
			}
			return uint16(m.coordinateRotationCandidate(lease.scope.id, int(attempt))), nil
		},
	)
	if err != nil {
		return technicalRotation{}, err
	}
	return coordinateRotationFromIndex(int(rotationIndex)), nil
}

func (m *TechnicalMapper) coordinateRotationCandidate(scopeID string, attempt int) int {
	startDigest := m.derive(scopeID, "COORDINATES", "rotation", 0)
	strideDigest := m.derive(scopeID, "COORDINATES", "rotation", 1)
	start := int(binary.BigEndian.Uint64(startDigest[:8]) % coordinateSymmetryCount)
	stride := 1 + int(binary.BigEndian.Uint64(strideDigest[:8])%(coordinateSymmetryCount-1))
	for greatestCommonDivisor(stride, coordinateSymmetryCount) != 1 {
		stride++
		if stride == coordinateSymmetryCount {
			stride = 1
		}
	}
	return (start + attempt*stride) % coordinateSymmetryCount
}

func greatestCommonDivisor(left, right int) int {
	for right != 0 {
		left, right = right, left%right
	}
	return left
}

func coordinateRotationFromIndex(rotationIndex int) technicalRotation {
	// These 7,199 non-identity proper rotations preserve every accepted
	// decimal latitude/longitude grid: 3,599 rotations about the polar
	// axis in 0.1-degree steps, plus 3,600 equatorial half-turns whose
	// doubled axis longitude advances in 0.1-degree steps.
	// Applying them through Rodrigues preserves spherical distance, while
	// their coordinate-space symmetries avoid precision-losing rounding.
	if rotationIndex < 3599 {
		return technicalRotation{
			axis:  [3]float64{0, 0, 1},
			angle: float64(rotationIndex+1) * math.Pi / 1800,
		}
	}

	equatorialIndex := rotationIndex - 3599
	axisLongitude := float64(equatorialIndex) * math.Pi / 3600
	return technicalRotation{
		axis:  [3]float64{math.Cos(axisLongitude), math.Sin(axisLongitude), 0},
		angle: math.Pi,
	}
}

func rotateTechnicalCoordinate(coordinate technicalCoordinate, rotation technicalRotation) string {
	cosLatitude := math.Cos(coordinate.latitude)
	vector := [3]float64{
		cosLatitude * math.Cos(coordinate.longitude),
		cosLatitude * math.Sin(coordinate.longitude),
		math.Sin(coordinate.latitude),
	}
	axis := rotation.axis
	dot := axis[0]*vector[0] + axis[1]*vector[1] + axis[2]*vector[2]
	cross := [3]float64{
		axis[1]*vector[2] - axis[2]*vector[1],
		axis[2]*vector[0] - axis[0]*vector[2],
		axis[0]*vector[1] - axis[1]*vector[0],
	}
	cosine := math.Cos(rotation.angle)
	sine := math.Sin(rotation.angle)
	oneMinusCosine := 1 - cosine
	rotated := [3]float64{
		vector[0]*cosine + cross[0]*sine + axis[0]*dot*oneMinusCosine,
		vector[1]*cosine + cross[1]*sine + axis[1]*dot*oneMinusCosine,
		vector[2]*cosine + cross[2]*sine + axis[2]*dot*oneMinusCosine,
	}

	latitude := math.Asin(max(-1, min(1, rotated[2]))) * 180 / math.Pi
	longitude := math.Atan2(rotated[1], rotated[0]) * 180 / math.Pi
	latitudeHemisphere := "N"
	if latitude < 0 {
		latitudeHemisphere = "S"
	}
	longitudeHemisphere := "E"
	if longitude < 0 {
		longitudeHemisphere = "W"
	}
	return strconv.FormatFloat(math.Abs(latitude), 'f', coordinate.latitudePrecision, 64) +
		coordinate.latitudeDecoration + latitudeHemisphere + coordinate.separator +
		strconv.FormatFloat(math.Abs(longitude), 'f', coordinate.longitudePrecision, 64) +
		coordinate.longitudeDecoration + longitudeHemisphere
}

func (m *TechnicalMapper) mapIP(
	lease *ScopeLease,
	entity string,
	original string,
	provenance Provenance,
) (string, error) {
	addr, sourceBits, explicit, err := parseTechnicalIP(entity, original)
	if err != nil {
		return "", err
	}

	base, minimumBits, standaloneBits := technicalIPRange(entity)
	relationBits := standaloneBits
	if explicit {
		relationBits = sourceBits
		if relationBits < minimumBits {
			return "", fmt.Errorf("%s prefix /%d is broader than /%d: %w", entity, relationBits, minimumBits, errTechnicalValue)
		}
	}

	sourceRelation := netip.PrefixFrom(addr, relationBits).Masked()
	relationKey := entity + ":" + sourceRelation.String()
	targetRelation, _, err := lease.GetOrCreateRelation(relationKey, func(attempt uint32) (string, error) {
		candidate, candidateErr := m.ipRelationCandidate(
			lease.scope.id,
			entity,
			sourceRelation.String(),
			base,
			relationBits,
			attempt,
		)
		if candidateErr != nil {
			return "", candidateErr
		}
		return candidate.String(), nil
	})
	if err != nil {
		if errors.Is(err, errCandidateExhausted) {
			return "", &TechnicalCapacityError{Entity: entity, PrefixBits: relationBits}
		}
		return "", err
	}

	syntheticRelation, err := netip.ParsePrefix(targetRelation)
	if err != nil {
		return "", fmt.Errorf("stored %s relation: %w", entity, errTechnicalValue)
	}
	syntheticAddr := preserveIPHostOffset(syntheticRelation.Masked().Addr(), addr, relationBits)
	synthetic := syntheticAddr.String()
	if explicit {
		synthetic = netip.PrefixFrom(syntheticAddr, sourceBits).String()
	}

	entry, _, err := lease.GetOrCreate(entity, original, provenance, func(attempt uint32) (string, error) {
		if attempt != 0 {
			return "", &TechnicalCapacityError{Entity: entity, PrefixBits: relationBits}
		}
		return synthetic, nil
	})
	if err != nil {
		return "", err
	}
	return entry.Synthetic, nil
}

func parseTechnicalIP(entity, original string) (netip.Addr, int, bool, error) {
	if strings.Contains(original, "/") {
		prefix, err := netip.ParsePrefix(original)
		if err != nil || (entity == "IPv4") != prefix.Addr().Is4() {
			return netip.Addr{}, 0, false, fmt.Errorf("%s: %w", entity, errTechnicalValue)
		}
		return prefix.Addr(), prefix.Bits(), true, nil
	}

	addr, err := netip.ParseAddr(original)
	if err != nil || (entity == "IPv4") != addr.Is4() {
		return netip.Addr{}, 0, false, fmt.Errorf("%s: %w", entity, errTechnicalValue)
	}
	return addr, addr.BitLen(), false, nil
}

func technicalIPRange(entity string) (netip.Prefix, int, int) {
	if entity == "IPv4" {
		// RFC 2544 reserves 198.18.0.0/15 for benchmark testing.
		// https://www.rfc-editor.org/rfc/rfc2544
		return netip.MustParsePrefix("198.18.0.0/15"), 15, 24
	}
	// RFC 3849 reserves 2001:db8::/32 for documentation.
	// https://www.rfc-editor.org/rfc/rfc3849
	return netip.MustParsePrefix("2001:db8::/32"), 32, 64
}

func (m *TechnicalMapper) ipRelationCandidate(
	scopeID string,
	entity string,
	relation string,
	base netip.Prefix,
	prefixBits int,
	attempt uint32,
) (netip.Prefix, error) {
	selectorBits := prefixBits - base.Bits()
	if selectorBits < 32 && uint64(attempt) >= uint64(1)<<selectorBits {
		return netip.Prefix{}, &TechnicalCapacityError{Entity: entity, PrefixBits: prefixBits}
	}

	// An odd stride is coprime to the power-of-two selector space, so this
	// affine sequence visits every slot exactly once before wrapping. For
	// selector spaces wider than uint32, every callback attempt reachable
	// through ScopeLease is still collision-free.
	selector := m.ipSelector(scopeID, entity, relation, selectorBits, attempt)
	baseAddr := base.Masked().Addr()
	if baseAddr.Is4() {
		bytes := baseAddr.As4()
		copySelectorBits(bytes[:], base.Bits(), selectorBits, selector)
		return netip.PrefixFrom(netip.AddrFrom4(bytes), prefixBits).Masked(), nil
	}
	bytes := baseAddr.As16()
	copySelectorBits(bytes[:], base.Bits(), selectorBits, selector)
	return netip.PrefixFrom(netip.AddrFrom16(bytes), prefixBits).Masked(), nil
}

func (m *TechnicalMapper) ipSelector(
	scopeID string,
	entity string,
	relation string,
	selectorBits int,
	attempt uint32,
) *big.Int {
	modulus := new(big.Int).Lsh(big.NewInt(1), uint(selectorBits))
	startDigest := m.derive(scopeID, entity, relation, 0)
	strideDigest := m.derive(scopeID, entity, relation, 1)

	start := new(big.Int).SetBytes(startDigest[:])
	start.Mod(start, modulus)
	stride := new(big.Int).SetBytes(strideDigest[:])
	stride.Mod(stride, modulus)
	stride.SetBit(stride, 0, 1)

	selector := new(big.Int).Mul(stride, new(big.Int).SetUint64(uint64(attempt)))
	selector.Add(selector, start)
	return selector.Mod(selector, modulus)
}

func (m *TechnicalMapper) derive(scopeID, entity, relation string, attempt uint32) [sha256.Size]byte {
	mac := hmac.New(sha256.New, m.aliasKey)
	writeLengthPrefixed(mac, []byte(technicalHMACDomain))
	writeLengthPrefixed(mac, []byte(scopeID))
	writeLengthPrefixed(mac, []byte(entity))
	writeLengthPrefixed(mac, []byte(relation))
	var attemptBytes [4]byte
	binary.BigEndian.PutUint32(attemptBytes[:], attempt)
	writeLengthPrefixed(mac, attemptBytes[:])

	var digest [sha256.Size]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}

type byteWriter interface {
	Write([]byte) (int, error)
}

func writeLengthPrefixed(writer byteWriter, value []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}

func copySelectorBits(target []byte, start, count int, selector *big.Int) {
	for bit := 0; bit < count; bit++ {
		targetIndex := start + bit
		mask := byte(1 << (7 - uint(targetIndex%8)))
		if selector.Bit(count-1-bit) != 0 {
			target[targetIndex/8] |= mask
		} else {
			target[targetIndex/8] &^= mask
		}
	}
}

func preserveIPHostOffset(targetNetwork, source netip.Addr, prefixBits int) netip.Addr {
	if targetNetwork.Is4() {
		target := targetNetwork.As4()
		host := source.As4()
		copyHostBits(target[:], host[:], prefixBits)
		return netip.AddrFrom4(target)
	}
	target := targetNetwork.As16()
	host := source.As16()
	copyHostBits(target[:], host[:], prefixBits)
	return netip.AddrFrom16(target)
}

func copyHostBits(target, source []byte, prefixBits int) {
	for bit := prefixBits; bit < len(target)*8; bit++ {
		mask := byte(1 << (7 - uint(bit%8)))
		if source[bit/8]&mask != 0 {
			target[bit/8] |= mask
		} else {
			target[bit/8] &^= mask
		}
	}
}
