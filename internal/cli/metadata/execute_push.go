package metadata

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/rudrankriyam/App-Store-Connect-CLI/internal/cli/shared"
)

// PushExecutionOptions controls metadata push planning and apply behavior.
type PushExecutionOptions struct {
	CommandName  string
	AppID        string
	AppInfoID    string
	Version      string
	Platform     string
	Dir          string
	Include      string
	DryRun       bool
	AllowDeletes bool
	Confirm      bool
	ReviewDir    string
}

// ExecutePush computes and optionally applies a metadata push plan.
//
// This is the command-agnostic execution path used by metadata push and
// release orchestration.
func ExecutePush(ctx context.Context, opts PushExecutionOptions) (PushPlanResult, error) {
	result, _, err := ExecutePushWithWarnings(ctx, opts)
	return result, err
}

// ExecutePushWithWarnings computes a metadata push plan plus create-scope
// submission warnings for callers that need to emit them after output succeeds.
func ExecutePushWithWarnings(ctx context.Context, opts PushExecutionOptions) (PushPlanResult, []shared.SubmitReadinessCreateWarning, error) {
	errorPrefix := metadataMutationErrorPrefix(opts.CommandName)
	resolvedAppID := shared.ResolveAppID(opts.AppID)
	if resolvedAppID == "" {
		return PushPlanResult{}, nil, metadataRequiredInputError("--app", "--app is required (or set ASC_APP_ID)")
	}

	versionValue := strings.TrimSpace(opts.Version)
	if versionValue == "" {
		return PushPlanResult{}, nil, metadataRequiredInputError("--version", "--version is required")
	}

	dirValue := strings.TrimSpace(opts.Dir)
	if dirValue == "" {
		return PushPlanResult{}, nil, metadataRequiredInputError("--dir", "--dir is required")
	}
	if strings.TrimSpace(opts.ReviewDir) != "" && !opts.DryRun && !opts.Confirm {
		return PushPlanResult{}, nil, shared.UsageError("--confirm is required when applying an approved metadata plan")
	}

	platformValue := strings.TrimSpace(opts.Platform)
	if platformValue != "" {
		normalizedPlatform, err := shared.NormalizeAppStoreVersionPlatform(platformValue)
		if err != nil {
			return PushPlanResult{}, nil, shared.UsageError(err.Error())
		}
		platformValue = normalizedPlatform
	}

	includeValue := strings.TrimSpace(opts.Include)
	if includeValue == "" {
		includeValue = includeLocalizations
	}
	includes, err := parseIncludes(includeValue)
	if err != nil {
		return PushPlanResult{}, nil, shared.UsageError(err.Error())
	}

	localBundle, err := loadLocalMetadata(dirValue, versionValue)
	if err != nil {
		return PushPlanResult{}, nil, fmt.Errorf("%s: %w", errorPrefix, err)
	}
	if err := validateDefaultClearFields(localBundle, versionValue); err != nil {
		return PushPlanResult{}, nil, shared.UsageError(err.Error())
	}

	client, err := shared.GetASCClient()
	if err != nil {
		return PushPlanResult{}, nil, fmt.Errorf("%s: %w", errorPrefix, err)
	}

	type versionResolution struct {
		id    string
		state string
	}
	resolvedVersion, err := shared.RetryReadWithFreshTimeout(ctx, func(requestCtx context.Context) (versionResolution, error) {
		id, state, resolveErr := resolveVersionID(requestCtx, client, resolvedAppID, versionValue, platformValue)
		return versionResolution{id: id, state: state}, resolveErr
	})
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return PushPlanResult{}, nil, err
		}
		return PushPlanResult{}, nil, fmt.Errorf("%s: %w", errorPrefix, err)
	}
	versionIDValue := resolvedVersion.id
	versionStateValue := resolvedVersion.state
	appInfoIDValue, err := shared.RetryReadWithFreshTimeout(ctx, func(requestCtx context.Context) (string, error) {
		return resolveMetadataPushAppInfoID(
			requestCtx,
			client,
			opts.CommandName,
			resolvedAppID,
			strings.TrimSpace(opts.AppInfoID),
			versionValue,
			platformValue,
			dirValue,
			versionStateValue,
		)
	})
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return PushPlanResult{}, nil, err
		}
		return PushPlanResult{}, nil, fmt.Errorf("%s: %w", errorPrefix, err)
	}

	remoteAppInfoItems, err := fetchAppInfoLocalizations(ctx, client, appInfoIDValue)
	if err != nil {
		return PushPlanResult{}, nil, fmt.Errorf("%s: %w", errorPrefix, err)
	}
	remoteVersionItems, err := fetchVersionLocalizations(ctx, client, versionIDValue)
	if err != nil {
		return PushPlanResult{}, nil, fmt.Errorf("%s: %w", errorPrefix, err)
	}

	remoteAppInfo := make(map[string]AppInfoLocalization, len(remoteAppInfoItems))
	for _, item := range remoteAppInfoItems {
		locale := strings.TrimSpace(item.Attributes.Locale)
		if locale == "" {
			continue
		}
		remoteAppInfo[locale] = NormalizeAppInfoLocalization(AppInfoLocalization{
			Name:              item.Attributes.Name,
			Subtitle:          item.Attributes.Subtitle,
			PrivacyPolicyURL:  item.Attributes.PrivacyPolicyURL,
			PrivacyChoicesURL: item.Attributes.PrivacyChoicesURL,
			PrivacyPolicyText: item.Attributes.PrivacyPolicyText,
		})
	}

	remoteVersion := remoteVersionItemsToVersionMap(remoteVersionItems)

	localAppInfo := applyDefaultAppInfoFallback(localBundle.appInfo, localBundle.defaultAppInfo, remoteAppInfo, opts.AllowDeletes)
	localVersion := applyDefaultVersionFallback(localBundle.version, localBundle.defaultVersion, remoteVersion, opts.AllowDeletes)
	if err := validateVersionClearOnlyLocales(localVersion, remoteVersion); err != nil {
		return PushPlanResult{}, nil, shared.UsageError(err.Error())
	}
	if err := validateMetadataCreatePrerequisites(localAppInfo, remoteAppInfo); err != nil {
		return PushPlanResult{}, nil, shared.UsageError(err.Error())
	}
	warningMode := shared.SubmitReadinessCreateModePlanned
	if !opts.DryRun {
		warningMode = shared.SubmitReadinessCreateModeApplied
	}
	submitOpts := shared.SubmitReadinessOptions{}
	if versionCreateWarningsNeedUpdateContext(localVersion, remoteVersion) {
		readinessCtx, readinessCancel := shared.ContextWithTimeout(ctx)
		submitOpts = shared.ResolveSubmitReadinessOptionsForVersionBestEffort(readinessCtx, client, versionIDValue, resolvedAppID, platformValue)
		readinessCancel()
	}
	warnings := versionCreateWarningsForPatches(localVersion, remoteVersion, warningMode, submitOpts)

	adds, updates, deletes, appInfoCalls := buildScopePlan(
		appInfoDirName,
		"",
		appInfoPlanFields,
		appInfoToPlanFields(localAppInfo),
		appInfoToFieldMap(remoteAppInfo),
	)
	versionAdds, versionUpdates, versionDeletes, versionCalls := buildScopePlan(
		versionDirName,
		versionValue,
		versionPlanFields,
		versionToPlanFields(localVersion),
		versionToFieldMap(remoteVersion),
	)
	adds = append(adds, versionAdds...)
	updates = append(updates, versionUpdates...)
	deletes = append(deletes, versionDeletes...)

	sortPlanItems(adds)
	sortPlanItems(updates)
	sortPlanItems(deletes)

	apiCalls := buildAPICallSummary(appInfoCalls, versionCalls)

	result := PushPlanResult{
		AppID:     resolvedAppID,
		AppInfoID: appInfoIDValue,
		Version:   versionValue,
		VersionID: versionIDValue,
		Dir:       dirValue,
		DryRun:    opts.DryRun,
		Includes:  includes,
		Adds:      adds,
		Updates:   updates,
		Deletes:   deletes,
		APICalls:  apiCalls,
	}

	if strings.TrimSpace(opts.ReviewDir) != "" {
		if err := VerifyApprovedMetadataPlan(opts, result, opts.ReviewDir); err != nil {
			return PushPlanResult{}, warnings, err
		}
	}

	if opts.DryRun {
		return result, warnings, nil
	}

	if len(result.Deletes) > 0 {
		if !opts.AllowDeletes {
			return PushPlanResult{}, nil, shared.UsageError("--allow-deletes is required to apply delete operations")
		}
		if !opts.Confirm {
			return PushPlanResult{}, nil, shared.UsageError("--confirm is required when applying delete operations")
		}
	}
	if !opts.Confirm {
		for _, update := range result.Updates {
			if update.Reason == "field cleared locally" {
				return PushPlanResult{}, nil, shared.UsageError("--confirm is required when applying field clear operations")
			}
		}
	}

	actions, applyErr := applyMetadataPlan(
		ctx,
		client,
		appInfoIDValue,
		versionIDValue,
		versionValue,
		localAppInfo,
		localVersion,
		remoteAppInfoItems,
		remoteVersionItems,
		opts.AllowDeletes,
	)
	result.Actions = actions
	result.Total = len(actions)
	for _, action := range actions {
		if action.Status == "failed" {
			result.Failed++
			continue
		}
		result.Succeeded++
	}
	result.Applied = applyErr == nil && result.Failed == 0

	if result.Failed > 0 {
		artifactPath, artifactErr := writeMetadataPushFailureArtifact(result, opts.CommandName)
		if artifactErr != nil {
			result.FailureArtifactError = artifactErr.Error()
		} else {
			result.FailureArtifactPath = artifactPath
		}
	}
	if !opts.DryRun {
		warnings = successfulVersionCreateWarnings(warnings, actions, remoteVersion)
	}

	if applyErr != nil {
		return result, warnings, fmt.Errorf("%s: %w", errorPrefix, applyErr)
	}
	return result, warnings, nil
}

// validateVersionClearOnlyLocales prevents a promotional-text clear from
// being silently treated as a no-op when its version localization is missing.
// Set fields can still create a localization; a null field on that new resource
// is already empty and remains omitted from the create payload.
func validateVersionClearOnlyLocales(
	localVersion map[string]versionLocalPatch,
	remoteVersion map[string]VersionLocalization,
) error {
	for _, locale := range sortedKeys(localVersion) {
		patch := localVersion[locale]
		if len(patch.setFields) > 0 || len(patch.clearFields) == 0 {
			continue
		}
		if _, exists := remoteVersion[locale]; !exists {
			return fmt.Errorf("version localization %q cannot be cleared because no existing localization was found", locale)
		}
	}
	return nil
}

func validateDefaultClearFields(bundle localMetadataBundle, version string) error {
	if bundle.defaultAppInfo != nil && len(bundle.defaultAppInfo.clearFields) > 0 {
		return fmt.Errorf("clear fields in app-info/default.json are not supported; move null values to an explicit locale file")
	}
	if bundle.defaultVersion != nil && len(bundle.defaultVersion.clearFields) > 0 {
		return fmt.Errorf("clear fields in version/%s/default.json are not supported; move null values to an explicit locale file", version)
	}
	return nil
}

func metadataMutationErrorPrefix(commandName string) string {
	name := strings.TrimSpace(commandName)
	if name == "" {
		name = "push"
	}
	return "metadata " + name
}
