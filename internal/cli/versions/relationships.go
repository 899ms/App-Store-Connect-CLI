package versions

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"

	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/asc"
	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/cli/shared"
)

type relationshipKind int

const (
	relationshipSingle relationshipKind = iota
	relationshipList
)

var appStoreVersionRelationshipKinds = map[string]relationshipKind{
	"ageRatingDeclaration":           relationshipSingle,
	"appStoreReviewDetail":           relationshipSingle,
	"appClipDefaultExperience":       relationshipSingle,
	"appStoreVersionExperiments":     relationshipList,
	"appStoreVersionExperimentsV2":   relationshipList,
	"appStoreVersionSubmission":      relationshipSingle,
	"customerReviews":                relationshipList,
	"routingAppCoverage":             relationshipSingle,
	"alternativeDistributionPackage": relationshipSingle,
	"gameCenterAppVersion":           relationshipSingle,
}

func paginationConflictParameter(limit int, next string, paginate bool) string {
	parameters := make([]string, 0, 3)
	if limit != 0 {
		parameters = append(parameters, "--limit")
	}
	if strings.TrimSpace(next) != "" {
		parameters = append(parameters, "--next")
	}
	if paginate {
		parameters = append(parameters, "--paginate")
	}
	if len(parameters) == 1 {
		return parameters[0]
	}
	return ""
}

// VersionsRelationshipsCommand returns the links subcommand.
func VersionsRelationshipsCommand() *ffcli.Command {
	fs := flag.NewFlagSet("versions links", flag.ExitOnError)

	versionID := fs.String("version-id", "", "App Store version ID (not an app ID; list IDs with \"asc versions list --app APP_ID\")")
	relType := fs.String("type", "", "Relationship type (required); must be one of: "+appStoreVersionRelationshipValues())
	limit := fs.Int("limit", 0, "Maximum results per page (1-200)")
	next := fs.String("next", "", "Fetch next page using a links.next URL")
	paginate := fs.Bool("paginate", false, "Automatically fetch all pages (aggregate results)")
	output := shared.BindOutputFlags(fs)

	return &ffcli.Command{
		Name:       "links",
		ShortUsage: "asc versions links --version-id \"VERSION_ID\" --type \"RELATIONSHIP\" [flags]",
		ShortHelp:  "List relationship linkages for an app store version.",
		LongHelp: `List relationship linkages for an app store version.

Examples:
  asc versions links --version-id "VERSION_ID" --type "appStoreReviewDetail"
  asc versions links --version-id "VERSION_ID" --type "appStoreVersionExperiments" --paginate`,
		FlagSet:   fs,
		UsageFunc: shared.DefaultUsageFunc,
		Exec: func(ctx context.Context, args []string) error {
			if *limit != 0 && (*limit < 1 || *limit > 200) {
				return shared.WithDiagnostic(
					shared.UsageError("versions links: --limit must be between 1 and 200"),
					shared.DiagnosticInvalidInput,
					"--limit",
				)
			}
			if err := shared.ValidateNextURL(*next); err != nil {
				return shared.WithDiagnostic(
					shared.UsageErrorf("versions links: %v", err),
					shared.DiagnosticInvalidInput,
					"--next",
				)
			}

			relationshipType := strings.TrimSpace(*relType)
			if relationshipType == "" {
				fmt.Fprintf(os.Stderr, "Error: --type is required; must be one of: %s\n", appStoreVersionRelationshipValues())
				return shared.MissingRequiredUsageError("--type")
			}

			kind, ok := appStoreVersionRelationshipKinds[relationshipType]
			if !ok {
				fmt.Fprintf(
					os.Stderr,
					"Error: --type %q is not a valid relationship type; must be one of: %s\n",
					relationshipType,
					appStoreVersionRelationshipValues(),
				)
				return shared.WithDiagnostic(flag.ErrHelp, shared.DiagnosticInvalidInput, "--type")
			}

			trimmedID := strings.TrimSpace(*versionID)
			trimmedNext := strings.TrimSpace(*next)
			if trimmedID == "" && trimmedNext == "" {
				fmt.Fprintln(os.Stderr, "Error: --version-id is required")
				return shared.MissingRequiredUsageError("--version-id")
			}

			if kind == relationshipSingle && (trimmedNext != "" || *paginate || *limit != 0) {
				fmt.Fprintln(os.Stderr, "Error: --limit, --next, and --paginate are only valid for to-many relationships")
				return shared.WithDiagnostic(
					flag.ErrHelp,
					shared.DiagnosticConflictingInput,
					paginationConflictParameter(*limit, trimmedNext, *paginate),
				)
			}

			client, err := shared.GetASCClient()
			if err != nil {
				return fmt.Errorf("versions links: %w", err)
			}

			requestCtx, cancel := shared.ContextWithTimeout(ctx)
			defer cancel()

			switch kind {
			case relationshipSingle:
				resp, err := getAppStoreVersionRelationship(requestCtx, client, relationshipType, trimmedID)
				if err != nil {
					return fmt.Errorf("versions links: %w", describeRelationshipLookupFailure(err, relationshipType, trimmedID))
				}
				return shared.PrintOutput(resp, *output.Output, *output.Pretty)
			case relationshipList:
				opts := []asc.LinkagesOption{
					asc.WithLinkagesLimit(*limit),
					asc.WithLinkagesNextURL(*next),
				}

				if *paginate {
					paginateOpts := append(opts, asc.WithLinkagesLimit(200))
					firstPage, err := getAppStoreVersionRelationshipList(requestCtx, client, relationshipType, trimmedID, paginateOpts...)
					if err != nil {
						return fmt.Errorf("versions links: failed to fetch: %w", describeRelationshipLookupFailure(err, relationshipType, trimmedID))
					}
					resp, err := asc.PaginateAll(requestCtx, firstPage, func(ctx context.Context, nextURL string) (asc.PaginatedResponse, error) {
						return getAppStoreVersionRelationshipList(ctx, client, relationshipType, trimmedID, asc.WithLinkagesNextURL(nextURL))
					})
					if err != nil {
						return fmt.Errorf("versions links: %w", describeRelationshipLookupFailure(err, relationshipType, trimmedID))
					}
					return shared.PrintOutput(resp, *output.Output, *output.Pretty)
				}

				resp, err := getAppStoreVersionRelationshipList(requestCtx, client, relationshipType, trimmedID, opts...)
				if err != nil {
					return fmt.Errorf("versions links: %w", describeRelationshipLookupFailure(err, relationshipType, trimmedID))
				}
				return shared.PrintOutput(resp, *output.Output, *output.Pretty)
			default:
				return fmt.Errorf("versions links: unsupported relationship type %q", relationshipType)
			}
		},
	}
}

func getAppStoreVersionRelationship(ctx context.Context, client *asc.Client, relationshipType, versionID string) (any, error) {
	switch relationshipType {
	case "ageRatingDeclaration":
		return client.GetAppStoreVersionAgeRatingDeclarationRelationship(ctx, versionID)
	case "appStoreReviewDetail":
		return client.GetAppStoreVersionReviewDetailRelationship(ctx, versionID)
	case "appClipDefaultExperience":
		return client.GetAppStoreVersionAppClipDefaultExperienceRelationship(ctx, versionID)
	case "appStoreVersionSubmission":
		return client.GetAppStoreVersionSubmissionRelationship(ctx, versionID)
	case "routingAppCoverage":
		return client.GetAppStoreVersionRoutingAppCoverageRelationship(ctx, versionID)
	case "alternativeDistributionPackage":
		return client.GetAppStoreVersionAlternativeDistributionPackageRelationship(ctx, versionID)
	case "gameCenterAppVersion":
		return client.GetAppStoreVersionGameCenterAppVersionRelationship(ctx, versionID)
	default:
		return nil, fmt.Errorf("unsupported relationship type %q", relationshipType)
	}
}

func getAppStoreVersionRelationshipList(ctx context.Context, client *asc.Client, relationshipType, versionID string, opts ...asc.LinkagesOption) (asc.PaginatedResponse, error) {
	switch relationshipType {
	case "appStoreVersionExperiments":
		return client.GetAppStoreVersionExperimentsRelationships(ctx, versionID, opts...)
	case "appStoreVersionExperimentsV2":
		return client.GetAppStoreVersionExperimentsV2Relationships(ctx, versionID, opts...)
	case "customerReviews":
		return client.GetAppStoreVersionCustomerReviewsRelationships(ctx, versionID, opts...)
	default:
		return nil, fmt.Errorf("unsupported relationship type %q", relationshipType)
	}
}

func appStoreVersionRelationshipList() []string {
	relationships := make([]string, 0, len(appStoreVersionRelationshipKinds))
	for key := range appStoreVersionRelationshipKinds {
		relationships = append(relationships, key)
	}
	sort.Strings(relationships)
	return relationships
}

// appStoreVersionRelationshipValues renders the accepted --type values so the
// flag help and both --type usage errors always agree.
func appStoreVersionRelationshipValues() string {
	return strings.Join(appStoreVersionRelationshipList(), ", ")
}

// describeRelationshipLookupFailure names the resource App Store Connect could
// not find so a 404 says whether the version ID is unknown or the relationship
// linkage is missing. A 404 that names neither, and every other failure, is
// returned unchanged.
func describeRelationshipLookupFailure(err error, relationshipType, versionID string) error {
	if err == nil || !asc.IsNotFound(err) {
		return err
	}
	if asc.IsMissingResourceOfType(err, "appStoreVersions") {
		if versionID == "" {
			return shared.NewErrorWithCause(
				errors.New("the app store version referenced by --next was not found"),
				err,
			)
		}
		return shared.NewErrorWithCause(
			fmt.Errorf(
				`app store version %q was not found; --version-id expects an App Store version ID, not an app ID (list them with: asc versions list --app "APP_ID")`,
				versionID,
			),
			err,
		)
	}
	if !namesRelationshipResource(err, relationshipType) {
		return err
	}
	if versionID == "" {
		return fmt.Errorf("%s relationship was not found: %w", relationshipType, err)
	}
	return fmt.Errorf("%s relationship was not found for app store version %q: %w", relationshipType, versionID, err)
}

// namesRelationshipResource reports whether Apple's 404 detail names the
// resource behind relationshipType, which Apple spells either exactly like the
// relationship or as its plural. Any other 404 keeps its original message so an
// unrelated failure is not relabeled as a missing relationship.
func namesRelationshipResource(err error, relationshipType string) bool {
	return asc.IsMissingResourceOfType(err, relationshipType) ||
		asc.IsMissingResourceOfType(err, relationshipType+"s")
}
