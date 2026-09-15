package shared

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/asc"
)

// buildBetaGroupPreflightBudget bounds the best-effort state reads. Without it
// a slow or hanging read could consume the whole command deadline, which would
// leave no time for the assignment the preflight is supposed to fall back to.
const buildBetaGroupPreflightBudget = 20 * time.Second

// buildBetaGroupPreflightBudgetOverride lets tests shrink the budget.
var buildBetaGroupPreflightBudgetOverride time.Duration

// SetBuildBetaGroupPreflightBudgetForTesting shrinks the preflight read budget.
// It returns a restore function to reset the previous value.
func SetBuildBetaGroupPreflightBudgetForTesting(budget time.Duration) func() {
	previous := buildBetaGroupPreflightBudgetOverride
	buildBetaGroupPreflightBudgetOverride = budget
	return func() {
		buildBetaGroupPreflightBudgetOverride = previous
	}
}

// buildBetaGroupPreflightContext bounds the state reads and reserves at least
// half of any remaining command deadline for the assignment itself, so a slow
// read cannot starve the mutation it precedes.
func buildBetaGroupPreflightContext(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := buildBetaGroupPreflightBudget
	if buildBetaGroupPreflightBudgetOverride > 0 {
		budget = buildBetaGroupPreflightBudgetOverride
	}
	if deadline, ok := ctx.Deadline(); ok {
		half := time.Until(deadline) / 2
		if half <= 0 {
			return ctx, func() {}
		}
		if half < budget {
			budget = half
		}
	}
	return context.WithTimeout(ctx, budget)
}

// Build processing states relevant to beta-group assignment.
const (
	buildProcessingStateProcessing = "PROCESSING"
	buildProcessingStateFailed     = "FAILED"
	buildProcessingStateInvalid    = "INVALID"
)

// External beta states relevant to beta-group assignment. The full enum lives
// in docs/openapi/latest.json under ExternalBetaState.
const (
	externalBetaStateProcessing              = "PROCESSING"
	externalBetaStateProcessingException     = "PROCESSING_EXCEPTION"
	externalBetaStateMissingExportCompliance = "MISSING_EXPORT_COMPLIANCE"
	externalBetaStateExpired                 = "EXPIRED"
	externalBetaStateInExportComplianceRev   = "IN_EXPORT_COMPLIANCE_REVIEW"
	externalBetaStateReadyForBetaSubmission  = "READY_FOR_BETA_SUBMISSION"
	externalBetaStateBetaRejected            = "BETA_REJECTED"
	externalBetaStateNotApplicable           = "NOT_APPLICABLE"
)

// buildBetaGroupPreflightClient reads the build state that App Store Connect
// validates before it accepts a beta-group assignment.
type buildBetaGroupPreflightClient interface {
	GetBuild(ctx context.Context, buildID string) (*asc.BuildResponse, error)
	GetBuildBuildBetaDetail(ctx context.Context, buildID string) (*asc.BuildBetaDetailResponse, error)
}

// BuildBetaGroupAssignmentPlan reports which resolved groups a beta-group
// assignment will send to App Store Connect and which ones the caller's
// options skip.
type BuildBetaGroupAssignmentPlan struct {
	GroupsToAdd                    []ResolvedBetaGroup
	SkippedInternalGroups          []ResolvedBetaGroup
	SkippedInternalAllBuildsGroups []ResolvedBetaGroup
}

// GroupIDsToAdd returns the IDs the assignment will post.
func (p BuildBetaGroupAssignmentPlan) GroupIDsToAdd() []string {
	ids := make([]string, 0, len(p.GroupsToAdd))
	for _, group := range p.GroupsToAdd {
		ids = append(ids, group.ID)
	}
	return ids
}

// IncludesExternalGroup reports whether the assignment posts at least one
// external beta group, which is what triggers Apple's external-testing
// preconditions.
func (p BuildBetaGroupAssignmentPlan) IncludesExternalGroup() bool {
	for _, group := range p.GroupsToAdd {
		if !group.IsInternalGroup {
			return true
		}
	}
	return false
}

// PlanBuildBetaGroupAssignment splits resolved groups into the set that will be
// posted and the sets the caller's options skip.
func PlanBuildBetaGroupAssignment(groups []ResolvedBetaGroup, opts AddBuildBetaGroupsOptions) BuildBetaGroupAssignmentPlan {
	plan := BuildBetaGroupAssignmentPlan{
		GroupsToAdd:                    make([]ResolvedBetaGroup, 0, len(groups)),
		SkippedInternalGroups:          make([]ResolvedBetaGroup, 0, len(groups)),
		SkippedInternalAllBuildsGroups: make([]ResolvedBetaGroup, 0, len(groups)),
	}
	for _, group := range groups {
		switch {
		case group.IsInternalGroup && opts.SkipInternal:
			plan.SkippedInternalGroups = append(plan.SkippedInternalGroups, group)
		case group.IsInternalGroup && group.HasAccessToAllBuilds && opts.SkipInternalWithAllBuilds:
			plan.SkippedInternalAllBuildsGroups = append(plan.SkippedInternalAllBuildsGroups, group)
		default:
			plan.GroupsToAdd = append(plan.GroupsToAdd, group)
		}
	}
	return plan
}

// BuildBetaGroupPreflightOptions describes the caller for remediation text.
type BuildBetaGroupPreflightOptions struct {
	// OperationName prefixes returned errors, for example "builds add-groups".
	OperationName string
	// Submit reports whether the caller submits the build for beta app review
	// after the assignment, which suppresses the advisory review reminder.
	Submit bool
}

// buildBetaGroupPrecondition is one App Store Connect state requirement that
// beta-group assignment depends on.
type buildBetaGroupPrecondition struct {
	// Summary states which precondition failed, including the API state value.
	Summary string
	// Fix names the asc command that clears the precondition.
	Fix string
}

// PreflightBuildBetaGroupAssignment verifies the build state that App Store
// Connect checks before it accepts a beta-group assignment, so a failing
// precondition is reported as a named validation failure instead of a raw
// HTTP 422 from the relationship POST.
//
// State reads are best-effort: when a read fails the preflight warns on stderr
// and lets the assignment proceed, so the check never becomes a new failure
// mode of its own.
func PreflightBuildBetaGroupAssignment(
	ctx context.Context,
	client buildBetaGroupPreflightClient,
	buildID string,
	plan BuildBetaGroupAssignmentPlan,
	opts BuildBetaGroupPreflightOptions,
) error {
	buildID = strings.TrimSpace(buildID)
	if buildID == "" || len(plan.GroupsToAdd) == 0 {
		return nil
	}

	preflightCtx, cancelPreflight := buildBetaGroupPreflightContext(ctx)
	defer cancelPreflight()

	build, err := client.GetBuild(preflightCtx, buildID)
	if err != nil {
		warnBuildBetaGroupPreflightRead(buildID, err)
		return nil
	}
	if build == nil {
		return nil
	}

	if precondition, ok := buildProcessingPrecondition(buildID, build.Data.Attributes); ok {
		return reportBuildBetaGroupPrecondition(opts.OperationName, precondition)
	}

	if !plan.IncludesExternalGroup() {
		return nil
	}

	detail, err := client.GetBuildBuildBetaDetail(preflightCtx, buildID)
	if err != nil {
		warnBuildBetaGroupPreflightRead(buildID, err)
		return nil
	}
	if detail == nil {
		return nil
	}

	externalState := strings.ToUpper(strings.TrimSpace(detail.Data.Attributes.ExternalBuildState))
	if precondition, ok := externalBetaStatePrecondition(buildID, externalState, build.Data.Attributes); ok {
		return reportBuildBetaGroupPrecondition(opts.OperationName, precondition)
	}

	// READY_FOR_BETA_SUBMISSION accepts external group assignment; the build
	// only needs a beta review submission before testers receive it. Remind the
	// caller instead of blocking, because add-then-submit is the documented
	// way to enable external distribution.
	if externalState == externalBetaStateReadyForBetaSubmission && !opts.Submit {
		fmt.Fprintf(
			os.Stderr,
			"Note: build %s has not been submitted for beta app review (externalBuildState %s). "+
				"External testers receive it after: asc testflight review submit --build-id %q --confirm\n",
			buildID,
			externalState,
			buildID,
		)
	}

	return nil
}

func buildProcessingPrecondition(buildID string, attributes asc.BuildAttributes) (buildBetaGroupPrecondition, bool) {
	switch strings.ToUpper(strings.TrimSpace(attributes.ProcessingState)) {
	case buildProcessingStateProcessing:
		return buildBetaGroupPrecondition{
			Summary: fmt.Sprintf(
				"build %s is still processing (processingState %s) and cannot be assigned to beta groups yet",
				buildID,
				buildProcessingStateProcessing,
			),
			Fix: fmt.Sprintf("Wait for processing to finish, then retry: asc builds wait --build-id %q", buildID),
		}, true
	case buildProcessingStateFailed:
		return buildBetaGroupPrecondition{
			Summary: fmt.Sprintf(
				"build %s failed processing (processingState %s) and can never be assigned to beta groups",
				buildID,
				buildProcessingStateFailed,
			),
			Fix: "Upload a replacement build: asc builds upload --app \"APP_ID\" --file \"PATH_TO_IPA\"",
		}, true
	case buildProcessingStateInvalid:
		return buildBetaGroupPrecondition{
			Summary: fmt.Sprintf(
				"build %s is invalid (processingState %s) and cannot be assigned to beta groups",
				buildID,
				buildProcessingStateInvalid,
			),
			Fix: "Upload a replacement build: asc builds upload --app \"APP_ID\" --file \"PATH_TO_IPA\"",
		}, true
	}

	if expired, known := attributes.ExpiredValue(); known && expired {
		summary := fmt.Sprintf("build %s has expired and cannot be assigned to beta groups", buildID)
		if expiration := strings.TrimSpace(attributes.ExpirationDate); expiration != "" {
			summary = fmt.Sprintf(
				"build %s has expired (expirationDate %s) and cannot be assigned to beta groups",
				buildID,
				expiration,
			)
		}
		return buildBetaGroupPrecondition{
			Summary: summary,
			Fix:     "Pick a build that has not expired: asc builds list --app \"APP_ID\" --limit 5",
		}, true
	}

	return buildBetaGroupPrecondition{}, false
}

func externalBetaStatePrecondition(
	buildID string,
	externalState string,
	attributes asc.BuildAttributes,
) (buildBetaGroupPrecondition, bool) {
	switch externalState {
	case externalBetaStateMissingExportCompliance:
		return buildBetaGroupPrecondition{
			Summary: fmt.Sprintf(
				"build %s is missing an export-compliance declaration (externalBuildState %s), so external beta groups cannot be assigned",
				buildID,
				externalState,
			),
			Fix: fmt.Sprintf(
				"Declare encryption use: asc builds update --build-id %q --uses-non-exempt-encryption=false\n"+
					"Or assign an existing declaration: asc encryption declarations assign-builds --id \"DECLARATION_ID\" --build-id %q",
				buildID,
				buildID,
			),
		}, true
	case externalBetaStateProcessing:
		return buildBetaGroupPrecondition{
			Summary: fmt.Sprintf(
				"build %s is still processing for external testing (externalBuildState %s), so external beta groups cannot be assigned yet",
				buildID,
				externalState,
			),
			Fix: fmt.Sprintf("Wait for processing to finish, then retry: asc builds wait --build-id %q", buildID),
		}, true
	case externalBetaStateProcessingException:
		return buildBetaGroupPrecondition{
			Summary: fmt.Sprintf(
				"build %s hit a processing exception for external testing (externalBuildState %s), so external beta groups cannot be assigned",
				buildID,
				externalState,
			),
			Fix: "Upload a replacement build: asc builds upload --app \"APP_ID\" --file \"PATH_TO_IPA\"",
		}, true
	case externalBetaStateInExportComplianceRev:
		return buildBetaGroupPrecondition{
			Summary: fmt.Sprintf(
				"build %s is in export-compliance review (externalBuildState %s), so external beta groups cannot be assigned yet",
				buildID,
				externalState,
			),
			Fix: fmt.Sprintf("Re-check the state and retry once review finishes: asc builds info --build-id %q", buildID),
		}, true
	case externalBetaStateExpired:
		return buildBetaGroupPrecondition{
			Summary: fmt.Sprintf(
				"build %s has expired for external testing (externalBuildState %s), so external beta groups cannot be assigned",
				buildID,
				externalState,
			),
			Fix: "Pick a build that has not expired: asc builds list --app \"APP_ID\" --limit 5",
		}, true
	case externalBetaStateBetaRejected:
		return buildBetaGroupPrecondition{
			Summary: fmt.Sprintf(
				"build %s did not pass beta app review (externalBuildState %s), so external beta groups cannot be assigned",
				buildID,
				externalState,
			),
			Fix: fmt.Sprintf(
				"Address the rejection in App Store Connect, then resubmit: asc testflight review submit --build-id %q --confirm",
				buildID,
			),
		}, true
	case externalBetaStateNotApplicable:
		summary := fmt.Sprintf(
			"build %s is not eligible for external testing (externalBuildState %s), so external beta groups cannot be assigned",
			buildID,
			externalState,
		)
		if audience := strings.TrimSpace(string(attributes.BuildAudienceType)); audience != "" {
			summary = fmt.Sprintf(
				"build %s is not eligible for external testing (externalBuildState %s, buildAudienceType %s), so external beta groups cannot be assigned",
				buildID,
				externalState,
				audience,
			)
		}
		return buildBetaGroupPrecondition{
			Summary: summary,
			Fix:     "Add internal groups only with --skip-internal omitted, or upload an App Store eligible build: asc builds upload --app \"APP_ID\" --file \"PATH_TO_IPA\"",
		}, true
	}

	return buildBetaGroupPrecondition{}, false
}

// reportBuildBetaGroupPrecondition prints the failing precondition with its
// remediation and returns a usage-class validation failure (exit code 2).
func reportBuildBetaGroupPrecondition(operationName string, precondition buildBetaGroupPrecondition) error {
	message := precondition.Summary
	if operation := strings.TrimSpace(operationName); operation != "" {
		message = fmt.Sprintf("%s: %s", operation, precondition.Summary)
	}

	fmt.Fprintf(os.Stderr, "Error: %s\n", SanitizeTerminal(message))
	if fix := strings.TrimSpace(precondition.Fix); fix != "" {
		fmt.Fprintln(os.Stderr, SanitizeTerminal(fix))
	}

	return WithDiagnostic(
		NewReportedUsageError(UsageErrorOther, message),
		DiagnosticStateNotReady,
		"",
	)
}

func warnBuildBetaGroupPreflightRead(buildID string, err error) {
	fmt.Fprintf(
		os.Stderr,
		"Warning: could not verify build %s state before adding beta groups: %v\n",
		buildID,
		err,
	)
}

// ReportBuildBetaGroupAssignmentFailure classifies a failed beta-group
// assignment. App Store Connect answers an unsatisfied state precondition with
// HTTP 422, so the Apple-supplied detail is rendered as a state failure with
// the same exit code instead of a generic API error.
func ReportBuildBetaGroupAssignmentFailure(buildID, operationName string, err error) error {
	if err == nil {
		return nil
	}

	apiErr, ok := errors.AsType[*asc.APIError](err)
	if !ok || apiErr == nil || apiErr.StatusCode != http.StatusUnprocessableEntity {
		if operation := strings.TrimSpace(operationName); operation != "" {
			return fmt.Errorf("%s: failed to add groups: %w", operation, err)
		}
		return fmt.Errorf("failed to add groups: %w", err)
	}

	detail := appleUnprocessableDetail(apiErr)
	message := fmt.Sprintf(
		"App Store Connect rejected adding beta groups to build %s (HTTP 422): %s",
		strings.TrimSpace(buildID),
		detail,
	)
	if operation := strings.TrimSpace(operationName); operation != "" {
		message = fmt.Sprintf("%s: %s", operation, message)
	}

	fmt.Fprintf(os.Stderr, "Error: %s\n", SanitizeTerminal(message))
	fmt.Fprintf(
		os.Stderr,
		"Re-check the build state before retrying: asc builds info --build-id %q\n",
		strings.TrimSpace(buildID),
	)

	return WithDiagnostic(
		NewReportedError(NewValidationError(NewErrorWithCause(fmt.Errorf("%s", message), err))),
		DiagnosticStateNotReady,
		"",
	)
}

// appleUnprocessableDetail renders Apple's own explanation for a 422, falling
// back through detail, title, and code so the message never degrades into a
// bare status line.
func appleUnprocessableDetail(apiErr *asc.APIError) string {
	code := strings.TrimSpace(apiErr.Code)
	parts := make([]string, 0, 2)
	if detail := strings.TrimSpace(apiErr.Detail); detail != "" {
		parts = append(parts, detail)
	} else if title := strings.TrimSpace(apiErr.Title); title != "" {
		parts = append(parts, title)
	}
	if code != "" {
		parts = append(parts, fmt.Sprintf("(%s)", code))
	}
	if len(parts) == 0 {
		return "App Store Connect returned no error detail"
	}
	return strings.Join(parts, " ")
}
