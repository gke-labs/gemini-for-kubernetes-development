package tasks

func GetReviewScript() ([]byte, error) {
	return scriptsFS.ReadFile("review.sh")
}
