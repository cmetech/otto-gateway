package privacy

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// #nosec G101 -- this is the reserved generated-label grammar, not a credential.
const oneWaySecretLabelPatternText = `\[SECRET:[A-Z0-9_]+_[A-F0-9]{12}\]`

var oneWaySecretLabelPattern = regexp.MustCompile(oneWaySecretLabelPatternText)

type secretPattern struct {
	entity       string
	re           *regexp.Regexp
	valueCapture int
}

var secretValuePatterns = []secretPattern{
	{
		entity: "PRIVATE_KEY",
		re:     regexp.MustCompile(`(?s)-----BEGIN (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----.*?-----END (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----`),
	},
	{
		entity: "PROXY_AUTHORIZATION",
		re:     regexp.MustCompile(`(?i)proxy-authorization\s*:\s*(?:bearer|basic)\s+[A-Za-z0-9._~+/=-]+`),
	},
	{
		entity: "AUTHORIZATION",
		re:     regexp.MustCompile(`(?i)authorization\s*:\s*(?:bearer|basic)\s+[A-Za-z0-9._~+/=-]+`),
	},
	{
		entity:       "BEARER_TOKEN",
		re:           regexp.MustCompile(`(?i)\bbearer[ \t]+([A-Za-z0-9._~+/=-]+)`),
		valueCapture: 1,
	},
	{
		entity: "GITHUB_TOKEN",
		re:     regexp.MustCompile(`\b(?:ghp_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})\b`),
	},
	{
		entity: "GITLAB_TOKEN",
		re:     regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`),
	},
	{
		entity: "OPENAI_API_KEY",
		re:     regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_-]{32,}\b`),
	},
}

var urlSchemePattern = regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://`)

func detectSecretValues(value string) []Finding {
	var candidates []Finding
	registryOrder := 0
	for _, pattern := range secretValuePatterns {
		for _, spans := range pattern.re.FindAllStringSubmatchIndex(value, -1) {
			start, end := spans[0], spans[1]
			if pattern.valueCapture > 0 {
				start = spans[pattern.valueCapture*2]
				end = spans[pattern.valueCapture*2+1]
			}
			candidates = append(candidates, secretFinding(pattern.entity, start, end, registryOrder))
		}
		registryOrder++
	}
	for _, span := range findCredentialURLs(value) {
		candidates = append(candidates, secretFinding("CREDENTIAL_URL", span[0], span[1], registryOrder))
	}
	registryOrder++

	for _, assignment := range findStructuredAssignments(value) {
		key := assignment.key
		words := normalizeKeyWords(key)
		if !isSecretKeyWords(words) {
			continue
		}
		if isRedactedAssignment(value, assignment.valueStart) {
			continue
		}
		candidates = append(candidates, secretFinding(entityForKey(key), assignment.valueStart, assignment.valueEnd, registryOrder))
		registryOrder++
	}

	return nonOverlappingSecretFindings(candidates)
}

func isRedactedAssignment(value string, start int) bool {
	if start < 0 || start >= len(value) {
		return false
	}

	cursor := start
	if end, ok := credentialSchemeEnd(value, cursor); ok {
		cursor = end
	}
	end := cursor
	switch {
	case strings.HasPrefix(value[cursor:], redactedSecret):
		end += len(redactedSecret)
	case value[cursor] == '[':
		closeIndex := strings.IndexByte(value[cursor:], ']')
		if closeIndex < 0 {
			return false
		}
		end += closeIndex + 1
		if !secretTokenPattern.MatchString(value[cursor:end]) {
			return false
		}
	default:
		return false
	}
	return end == len(value) || isUnquotedAssignmentDelimiter(value[end])
}

func credentialSchemeEnd(value string, start int) (int, bool) {
	for _, scheme := range []string{"bearer", "basic"} {
		end := start + len(scheme)
		if end > len(value) || !strings.EqualFold(value[start:end], scheme) {
			continue
		}
		cursor := end
		for cursor < len(value) && (value[cursor] == ' ' || value[cursor] == '\t') {
			cursor++
		}
		if cursor > end {
			return cursor, true
		}
	}
	return start, false
}

func findCredentialURLs(value string) [][2]int {
	var spans [][2]int
	for _, scheme := range urlSchemePattern.FindAllStringIndex(value, -1) {
		hardEnd := scheme[1]
		for hardEnd < len(value) && !isURLBoundary(value[hardEnd]) {
			hardEnd++
		}

		end, ok := credentialURLPrefixEnd(value, scheme[0], scheme[1], hardEnd)
		if !ok {
			continue
		}
		spans = append(spans, [2]int{scheme[0], end})
	}
	return spans
}

func credentialURLPrefixEnd(value string, start, schemeEnd, hardEnd int) (int, bool) {
	return credentialURLPrefixEndWithValidator(value, start, schemeEnd, hardEnd, isCredentialURL)
}

func credentialURLPrefixEndWithValidator(value string, start, schemeEnd, hardEnd int, validate func(string) bool) (int, bool) {
	candidateEnd := hardEnd
	inAuthority := true
	userinfoComplete := false
	hostStart := schemeEnd
	inBracketedAuthority := false

scan:
	for cursor := schemeEnd; cursor < hardEnd; cursor++ {
		switch value[cursor] {
		case '@':
			if inAuthority && !inBracketedAuthority {
				userinfoComplete = true
				hostStart = cursor + 1
			}
		case '[':
			if inAuthority && cursor == hostStart {
				inBracketedAuthority = true
			}
		case ']':
			if inBracketedAuthority {
				inBracketedAuthority = false
				continue
			}
			candidateEnd = cursor
			break scan
		case ',':
			if userinfoComplete && !inBracketedAuthority {
				candidateEnd = cursor
				break scan
			}
		case '/', '?', '#':
			if !inBracketedAuthority {
				inAuthority = false
			}
		}
	}

	end := trimURLSuffix(value, schemeEnd, candidateEnd)
	return end, end > schemeEnd && validate(value[start:end])
}

func trimURLSuffix(value string, schemeEnd, end int) int {
	for end > schemeEnd && isTrailingProsePunctuation(value[end-1]) {
		end--
	}
	return end
}

func isCredentialURL(candidate string) bool {
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Host == "" || parsed.User == nil || parsed.User.Username() == "" {
		return false
	}
	_, hasPassword := parsed.User.Password()
	return hasPassword
}

func isURLBoundary(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n' ||
		value == '"' || value == '\'' || value == '}' ||
		value == '<' || value == '>'
}

func isTrailingProsePunctuation(value byte) bool {
	return value == '.' || value == ',' || value == ';' || value == '!' || value == ')'
}

type structuredAssignment struct {
	key                  string
	valueStart, valueEnd int
}

func findStructuredAssignments(value string) []structuredAssignment {
	var assignments []structuredAssignment
	oneWayLabels := oneWaySecretLabelPattern.FindAllStringIndex(value, -1)
	labelIndex := 0
	for start := 0; start < len(value); start++ {
		for labelIndex < len(oneWayLabels) && oneWayLabels[labelIndex][1] <= start {
			labelIndex++
		}
		if labelIndex < len(oneWayLabels) && start >= oneWayLabels[labelIndex][0] {
			start = oneWayLabels[labelIndex][1] - 1
			continue
		}
		keyStart := start
		cliPrefix := false
		if value[keyStart] == '-' && keyStart+2 < len(value) && value[keyStart+1] == '-' {
			keyStart += 2
			cliPrefix = true
		}
		if !isASCIIAlpha(value[keyStart]) || !cliPrefix && keyStart > 0 && isAssignmentKeyByte(value[keyStart-1]) {
			continue
		}

		keyEnd := keyStart + 1
		for keyEnd < len(value) && isAssignmentKeyByte(value[keyEnd]) {
			keyEnd++
		}
		cursor := keyEnd
		if cursor < len(value) && (value[cursor] == '\'' || value[cursor] == '"') {
			cursor++
		}
		for cursor < len(value) && (value[cursor] == ' ' || value[cursor] == '\t') {
			cursor++
		}
		if cursor >= len(value) || value[cursor] != ':' && value[cursor] != '=' {
			continue
		}
		cursor++
		for cursor < len(value) && (value[cursor] == ' ' || value[cursor] == '\t') {
			cursor++
		}
		if cursor >= len(value) {
			continue
		}

		valueStart := cursor
		valueEnd := cursor
		if quote := value[cursor]; quote == '\'' || quote == '"' {
			valueStart++
			valueEnd = valueStart
			for valueEnd < len(value) {
				if value[valueEnd] == quote && !isEscapedAt(value, valueEnd) {
					break
				}
				valueEnd++
			}
		} else {
			for valueEnd < len(value) && !isUnquotedAssignmentDelimiter(value[valueEnd]) {
				valueEnd++
			}
		}
		if valueEnd == valueStart {
			continue
		}

		assignments = append(assignments, structuredAssignment{
			key:        strings.TrimLeft(value[keyStart:keyEnd], "-"),
			valueStart: valueStart,
			valueEnd:   valueEnd,
		})
		start = keyEnd - 1
	}
	return assignments
}

func isASCIIAlpha(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func isAssignmentKeyByte(value byte) bool {
	return isASCIIAlpha(value) || value >= '0' && value <= '9' || value == '_' || value == '-' || value == '.'
}

func isUnquotedAssignmentDelimiter(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n' ||
		value == '"' || value == '\'' || value == ',' || value == '}' || value == ']'
}

func isEscapedAt(value string, index int) bool {
	backslashes := 0
	for index > 0 && value[index-1] == '\\' {
		backslashes++
		index--
	}
	return backslashes%2 == 1
}

func secretFinding(entity string, start, end, order int) Finding {
	return Finding{
		Entity:        entity,
		Category:      CategorySecret,
		Kind:          MatchHighConfidenceSecret,
		Start:         start,
		End:           end,
		RegistryOrder: order,
	}
}

func nonOverlappingSecretFindings(candidates []Finding) []Finding {
	return Arbitrate(candidates)
}

// Arbitrate resolves overlapping findings using confidence, span length, and
// registry order, then returns accepted spans in source order.
func Arbitrate(findings []Finding) []Finding {
	candidates := append([]Finding(nil), findings...)
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Kind != candidates[j].Kind {
			return candidates[i].Kind > candidates[j].Kind
		}
		leftLen := candidates[i].End - candidates[i].Start
		rightLen := candidates[j].End - candidates[j].Start
		if leftLen != rightLen {
			return leftLen > rightLen
		}
		return candidates[i].RegistryOrder < candidates[j].RegistryOrder
	})

	accepted := make([]Finding, 0, len(candidates))
	var intervals *intervalNode
	for _, candidate := range candidates {
		if intervalOverlaps(intervals, candidate) {
			continue
		}
		intervals = insertInterval(intervals, candidate)
		accepted = append(accepted, candidate)
	}

	sort.SliceStable(accepted, func(i, j int) bool {
		return accepted[i].Start < accepted[j].Start
	})
	return accepted
}

type intervalNode struct {
	finding     Finding
	left, right *intervalNode
	height      int
}

func intervalOverlaps(root *intervalNode, candidate Finding) bool {
	for root != nil {
		switch {
		case candidate.End <= root.finding.Start:
			root = root.left
		case candidate.Start >= root.finding.End:
			root = root.right
		default:
			return true
		}
	}
	return false
}

func insertInterval(root *intervalNode, finding Finding) *intervalNode {
	if root == nil {
		return &intervalNode{finding: finding, height: 1}
	}
	if finding.Start < root.finding.Start {
		root.left = insertInterval(root.left, finding)
	} else {
		root.right = insertInterval(root.right, finding)
	}
	return balanceIntervalNode(root)
}

func balanceIntervalNode(root *intervalNode) *intervalNode {
	root.height = max(intervalHeight(root.left), intervalHeight(root.right)) + 1
	balance := intervalHeight(root.left) - intervalHeight(root.right)
	if balance > 1 {
		if intervalHeight(root.left.left) < intervalHeight(root.left.right) {
			root.left = rotateIntervalLeft(root.left)
		}
		return rotateIntervalRight(root)
	}
	if balance < -1 {
		if intervalHeight(root.right.right) < intervalHeight(root.right.left) {
			root.right = rotateIntervalRight(root.right)
		}
		return rotateIntervalLeft(root)
	}
	return root
}

func rotateIntervalLeft(root *intervalNode) *intervalNode {
	pivot := root.right
	root.right = pivot.left
	pivot.left = root
	root.height = max(intervalHeight(root.left), intervalHeight(root.right)) + 1
	pivot.height = max(intervalHeight(pivot.left), intervalHeight(pivot.right)) + 1
	return pivot
}

func rotateIntervalRight(root *intervalNode) *intervalNode {
	pivot := root.left
	root.left = pivot.right
	pivot.right = root
	root.height = max(intervalHeight(root.left), intervalHeight(root.right)) + 1
	pivot.height = max(intervalHeight(pivot.left), intervalHeight(pivot.right)) + 1
	return pivot
}

func intervalHeight(root *intervalNode) int {
	if root == nil {
		return 0
	}
	return root.height
}
