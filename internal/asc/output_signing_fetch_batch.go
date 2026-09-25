package asc

// SigningFetchBatchResult is the receipt for signing fetch --match-extensions
// when more than one registered bundle ID matched.
type SigningFetchBatchResult struct {
	MatchedBundleIDs []string                   `json:"matchedBundleIds"`
	Results          []SigningFetchResult       `json:"results"`
	Failures         []SigningFetchBatchFailure `json:"failures,omitempty"`
}

// SigningFetchBatchFailure is one matched bundle ID whose fetch failed.
type SigningFetchBatchFailure struct {
	BundleID string `json:"bundleId"`
	Error    string `json:"error"`
}

func signingFetchBatchResultRender(result *SigningFetchBatchResult, render func([]string, [][]string)) error {
	var headers []string
	rows := make([][]string, 0, len(result.Results))
	for i := range result.Results {
		resultHeaders, resultRows := signingFetchResultRows(&result.Results[i])
		if len(resultHeaders) > len(headers) {
			headers = resultHeaders
		}
		rows = append(rows, resultRows...)
	}
	if headers == nil {
		headers, _ = signingFetchResultRows(&SigningFetchResult{})
	}
	for i := range rows {
		for len(rows[i]) < len(headers) {
			rows[i] = append(rows[i], "")
		}
	}
	render(headers, rows)
	if len(result.Failures) > 0 {
		failureRows := make([][]string, 0, len(result.Failures))
		for _, failure := range result.Failures {
			failureRows = append(failureRows, []string{failure.BundleID, failure.Error})
		}
		render([]string{"Failed Bundle ID", "Error"}, failureRows)
	}
	return nil
}
