package tasks

func GetReviewScript() ([]byte, error) {
	return getScriptWithDefaults("review.sh")
}
