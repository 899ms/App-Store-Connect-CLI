package sandbox

import (
	"context"
	"fmt"
	"net/mail"
	"sort"
	"strings"

	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/asc"
	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/ascterritory"
	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/cli/shared"
)

func validateSandboxEmail(value string) error {
	address := strings.TrimSpace(value)
	if address == "" {
		return fmt.Errorf("--email is required")
	}
	if _, err := mail.ParseAddress(address); err != nil {
		return fmt.Errorf("--email must be a valid email address")
	}
	return nil
}

// NormalizeSandboxTerritoryCode validates and normalizes a 3-letter App Store
// territory code used by sandbox tester operations.
func NormalizeSandboxTerritoryCode(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("--territory is required")
	}
	normalized, err := ascterritory.Normalize(trimmed)
	if err != nil {
		return "", fmt.Errorf("--territory must be a valid App Store territory code")
	}
	return normalized, nil
}

func normalizeSandboxTerritory(value string) (string, error) {
	return NormalizeSandboxTerritoryCode(value)
}

func normalizeSandboxTerritoryFilter(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return NormalizeSandboxTerritoryCode(value)
}

var sandboxRenewalRates = map[string]asc.SandboxTesterSubscriptionRenewalRate{
	string(asc.SandboxTesterRenewalEveryOneHour):        asc.SandboxTesterRenewalEveryOneHour,
	string(asc.SandboxTesterRenewalEveryThirtyMinutes):  asc.SandboxTesterRenewalEveryThirtyMinutes,
	string(asc.SandboxTesterRenewalEveryFifteenMinutes): asc.SandboxTesterRenewalEveryFifteenMinutes,
	string(asc.SandboxTesterRenewalEveryFiveMinutes):    asc.SandboxTesterRenewalEveryFiveMinutes,
	string(asc.SandboxTesterRenewalEveryThreeMinutes):   asc.SandboxTesterRenewalEveryThreeMinutes,
}

func normalizeSandboxRenewalRate(value string) (asc.SandboxTesterSubscriptionRenewalRate, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	normalized := strings.ToUpper(trimmed)
	normalized = strings.ReplaceAll(normalized, "-", "_")
	normalized = strings.ReplaceAll(normalized, " ", "_")
	if rate, ok := sandboxRenewalRates[normalized]; ok {
		return rate, nil
	}
	return "", fmt.Errorf("--subscription-renewal-rate must be one of: %s", strings.Join(sandboxRenewalRateValues(), ", "))
}

func sandboxRenewalRateValues() []string {
	values := make([]string, 0, len(sandboxRenewalRates))
	for key := range sandboxRenewalRates {
		values = append(values, key)
	}
	sort.Strings(values)
	return values
}

func findSandboxTesterByEmail(ctx context.Context, client *asc.Client, email string) (*asc.SandboxTesterResponse, error) {
	next := ""
	matches := make([]asc.Resource[asc.SandboxTesterAttributes], 0, 1)
	for {
		resp, err := client.GetSandboxTesters(
			ctx,
			asc.WithSandboxTestersEmail(email),
			asc.WithSandboxTestersLimit(200),
			asc.WithSandboxTestersNextURL(next),
		)
		if err != nil {
			return nil, err
		}
		if resp == nil {
			return nil, fmt.Errorf("empty sandbox testers response")
		}
		nextURL := strings.TrimSpace(resp.Links.Next)
		matches = append(matches, resp.Data...)
		if len(matches) > 1 {
			candidates := make([]shared.AmbiguousCandidate, 0, len(resp.Data))
			for _, tester := range matches {
				candidates = append(candidates, shared.AmbiguousCandidate{
					ID: strings.TrimSpace(tester.ID),
				})
			}
			ambiguous := &shared.AmbiguousSelectionError{
				Kind:        "sandbox tester",
				Description: fmt.Sprintf("email %q", strings.TrimSpace(email)),
				Flag:        "--id",
				Candidates:  candidates,
			}
			if nextURL != "" {
				return nil, shared.MarkAmbiguousSelectionSample(ambiguous)
			}
			return nil, ambiguous
		}
		if nextURL == "" {
			if len(matches) == 1 {
				return &asc.SandboxTesterResponse{Data: matches[0], Links: resp.Links}, nil
			}
			break
		}
		if err := shared.ValidateNextURL(nextURL); err != nil {
			return nil, err
		}
		next = nextURL
	}
	return nil, fmt.Errorf("no sandbox tester found for %q", strings.TrimSpace(email))
}

func findSandboxTesterIDByEmail(ctx context.Context, client *asc.Client, email string) (string, error) {
	response, err := findSandboxTesterByEmail(ctx, client, email)
	if err != nil {
		return "", err
	}
	return response.Data.ID, nil
}
