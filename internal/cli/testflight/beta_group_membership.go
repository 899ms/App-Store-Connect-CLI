package testflight

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/asc"
	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/cli/shared"
)

// betaGroupMembershipReadBack records, for one group, which of the requested
// testers a post-conflict read-back found already in the group.
type betaGroupMembershipReadBack struct {
	groupID string
	present []string
	missing []string
}

// satisfied reports whether every requested tester is already a member, which
// is the end state the add asked for.
func (r betaGroupMembershipReadBack) satisfied() bool {
	return len(r.present) > 0 && len(r.missing) == 0
}

// diagnostic renders the read-back for an error message so an operator can see
// which testers still need adding.
func (r betaGroupMembershipReadBack) diagnostic() string {
	parts := make([]string, 0, 2)
	if len(r.present) > 0 {
		parts = append(parts, fmt.Sprintf("already in group %s: %s", r.groupID, strings.Join(r.present, ", ")))
	}
	if len(r.missing) > 0 {
		parts = append(parts, fmt.Sprintf("not in group %s: %s", r.groupID, strings.Join(r.missing, ", ")))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, "; ") + ")"
}

// isBetaGroupMembershipConflict reports whether err is an App Store Connect
// HTTP 409 on a beta group membership write.
//
// Apple's code for "this tester is already in the group" is not documented and
// could not be captured: the codes observed on
// POST /v1/betaGroups/{id}/relationships/betaTesters are STATE_ERROR ("Tester(s)
// cannot be assigned", Apple Developer Forums thread 745785) and
// ENTITY_ERROR.RELATIONSHIP.INVALID, and STATE_ERROR is reported both for a
// tester the group already contains and for a tester that genuinely cannot be
// assigned. A code list would therefore either miss the real conflict or admit
// unrelated ones, so every 409 is only a candidate here and the caller's
// membership read-back stays the decisive check: the conflict is forgiven only
// when every requested tester is already a member, which is exactly the end
// state the add requested. Any other status (and any 409 whose read-back finds
// a tester missing) fails exactly as before.
//
// betaTesterGroupConflictAlreadySatisfied applies the same rule to the sibling
// POST /v1/betaTesters/{id}/relationships/betaGroups write used by the CSV
// importer.
func isBetaGroupMembershipConflict(err error) bool {
	return err != nil && errors.Is(err, asc.ErrConflict)
}

// readBackBetaGroupMembership resolves which requested testers are already in
// the group by reading each tester's beta group linkages. It is only called
// after a failed add, so the extra reads never touch the success path.
func readBackBetaGroupMembership(
	ctx context.Context,
	client *asc.Client,
	groupID string,
	testerIDs []string,
) (betaGroupMembershipReadBack, error) {
	result := betaGroupMembershipReadBack{groupID: groupID}
	if client == nil {
		return result, fmt.Errorf("client is required")
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return result, fmt.Errorf("groupID is required")
	}

	for _, testerID := range testerIDs {
		testerID = strings.TrimSpace(testerID)
		if testerID == "" {
			continue
		}

		groupIDs, err := betaTesterGroupIDs(ctx, client, testerID)
		if err != nil {
			return betaGroupMembershipReadBack{groupID: groupID}, err
		}
		if _, ok := groupIDs[groupID]; ok {
			result.present = append(result.present, testerID)
			continue
		}
		result.missing = append(result.missing, testerID)
	}
	return result, nil
}

// betaTesterGroupIDs returns every beta group the tester currently belongs to.
func betaTesterGroupIDs(ctx context.Context, client *asc.Client, testerID string) (map[string]struct{}, error) {
	requestCtx, cancel := shared.ContextWithTimeout(ctx)
	first, err := client.GetBetaTesterBetaGroupsRelationships(requestCtx, testerID, asc.WithLinkagesLimit(200))
	cancel()
	if err != nil {
		return nil, err
	}

	all, err := asc.PaginateAll(ctx, first, func(ctx context.Context, nextURL string) (asc.PaginatedResponse, error) {
		requestCtx, cancel := shared.ContextWithTimeout(ctx)
		defer cancel()
		return client.GetBetaTesterBetaGroupsRelationships(requestCtx, testerID, asc.WithLinkagesNextURL(nextURL))
	})
	if err != nil {
		return nil, err
	}
	resp, ok := all.(*asc.LinkagesResponse)
	if !ok || resp == nil {
		return nil, fmt.Errorf("unexpected beta tester group relationships response type")
	}

	groupIDs := make(map[string]struct{}, len(resp.Data))
	for _, linkage := range resp.Data {
		if groupID := strings.TrimSpace(linkage.ID); groupID != "" {
			groupIDs[groupID] = struct{}{}
		}
	}
	return groupIDs, nil
}
