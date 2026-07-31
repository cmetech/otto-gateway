package privacy

import (
	"regexp"
	"sort"
	"strings"
)

type secretPattern struct {
	entity string
	re     *regexp.Regexp
}

var secretValuePatterns = []secretPattern{
	{
		entity: "PRIVATE_KEY",
		re:     regexp.MustCompile(`(?s)-----BEGIN (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----.*?-----END (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----`),
	},
	{
		entity: "CREDENTIAL_URL",
		re:     regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^\s/:@]+:[^\s/@]+@[^\s]+`),
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

var structuredAssignmentPattern = regexp.MustCompile(`(?i)(?:--)?[A-Za-z][A-Za-z0-9_.-]*["']?\s*[:=]\s*["']?[^\s"',}]+`)

func detectSecretValues(value string) []Finding {
	var candidates []Finding
	registryOrder := 0
	for _, pattern := range secretValuePatterns {
		for _, span := range pattern.re.FindAllStringIndex(value, -1) {
			candidates = append(candidates, secretFinding(pattern.entity, span[0], span[1], registryOrder))
		}
		registryOrder++
	}

	for _, span := range structuredAssignmentPattern.FindAllStringIndex(value, -1) {
		assignment := value[span[0]:span[1]]
		separator := strings.IndexAny(assignment, ":=")
		if separator < 0 {
			continue
		}
		key := strings.Trim(strings.TrimLeft(strings.TrimSpace(assignment[:separator]), "-"), "\"'")
		words := normalizeKeyWords(key)
		if !containsCredentialCompound(words) && !containsAuthorizationName(words) {
			continue
		}
		candidates = append(candidates, secretFinding(entityForKey(key), span[0], span[1], registryOrder))
		registryOrder++
	}

	return nonOverlappingSecretFindings(candidates)
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
	for _, candidate := range candidates {
		overlaps := false
		for _, existing := range accepted {
			if candidate.Start < existing.End && existing.Start < candidate.End {
				overlaps = true
				break
			}
		}
		if !overlaps {
			accepted = append(accepted, candidate)
		}
	}

	sort.SliceStable(accepted, func(i, j int) bool {
		return accepted[i].Start < accepted[j].Start
	})
	return accepted
}
