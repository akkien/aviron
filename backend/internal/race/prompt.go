package race

import (
	"strings"

	"github.com/brianvoe/gofakeit/v7"
)

// generatePromptText returns wordCount random real-English words (from
// gofakeit's word corpus) joined by single spaces — exactly wordCount words,
// so the frontend's words-typed/target progress calculation reaches exactly
// 100% at completion.
func generatePromptText(wordCount int) string {
	words := make([]string, wordCount)
	for i := range words {
		words[i] = gofakeit.Word()
	}
	return strings.Join(words, " ")
}
