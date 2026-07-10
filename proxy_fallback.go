package main

func isRetryableUpstreamError(statusCode int, err error) bool {
	if err != nil {
		return true
	}
	if statusCode >= 500 && statusCode <= 599 {
		return true
	}
	return statusCode == 429 || statusCode == 408
}
