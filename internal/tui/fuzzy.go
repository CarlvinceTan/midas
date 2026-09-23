package tui

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
)

type FuzzyResult struct {
	Matches bool
	Score   float64
}

var fuzzyBoundary = regexp.MustCompile(`[\s\-_./:]`)

func FuzzyMatch(query, value string) FuzzyResult {
	queryLower, valueLower := strings.ToLower(query), strings.ToLower(value)
	match := func(candidate string) FuzzyResult {
		if candidate == "" {
			return FuzzyResult{Matches: true}
		}
		queryRunes, valueRunes := []rune(candidate), []rune(valueLower)
		if len(queryRunes) > len(valueRunes) {
			return FuzzyResult{}
		}
		queryIndex, last, consecutive := 0, -1, 0
		score := 0.0
		for index := 0; index < len(valueRunes) && queryIndex < len(queryRunes); index++ {
			if valueRunes[index] != queryRunes[queryIndex] {
				continue
			}
			boundary := index == 0 || fuzzyBoundary.MatchString(string(valueRunes[index-1]))
			if last == index-1 {
				consecutive++
				score -= float64(consecutive * 5)
			} else {
				consecutive = 0
				if last >= 0 {
					score += float64((index - last - 1) * 2)
				}
			}
			if boundary {
				score -= 10
			}
			score += float64(index) * .1
			last, queryIndex = index, queryIndex+1
		}
		if queryIndex < len(queryRunes) {
			return FuzzyResult{}
		}
		if candidate == valueLower {
			score -= 100
		}
		return FuzzyResult{Matches: true, Score: score}
	}
	primary := match(queryLower)
	if primary.Matches {
		return primary
	}
	swappedQuery := swappedAlphaNumeric(queryLower)
	if swappedQuery == "" {
		return primary
	}
	swapped := match(swappedQuery)
	if !swapped.Matches {
		return primary
	}
	swapped.Score += 5
	return swapped
}

func swappedAlphaNumeric(value string) string {
	runes := []rune(value)
	index := 0
	for index < len(runes) && runes[index] >= 'a' && runes[index] <= 'z' {
		index++
	}
	if index > 0 && index < len(runes) {
		for _, r := range runes[index:] {
			if r < '0' || r > '9' {
				return ""
			}
		}
		return string(runes[index:]) + string(runes[:index])
	}
	index = 0
	for index < len(runes) && runes[index] >= '0' && runes[index] <= '9' {
		index++
	}
	if index > 0 && index < len(runes) {
		for _, r := range runes[index:] {
			if r < 'a' || r > 'z' {
				return ""
			}
		}
		return string(runes[index:]) + string(runes[:index])
	}
	return ""
}

func FuzzyFilter[T any](items []T, query string, text func(T) string) []T {
	return fuzzyFilter(items, query, text, FuzzyMatch)
}

// FuzzyFilterWithTypos preserves the normal fuzzy ranking, then falls back to
// a single edit for command-style input. Keeping this separate avoids making
// broad model and provider searches unexpectedly permissive.
func FuzzyFilterWithTypos[T any](items []T, query string, text func(T) string) []T {
	return fuzzyFilter(items, query, text, fuzzyMatchWithTypos)
}

func fuzzyFilter[T any](items []T, query string, text func(T) string, match func(string, string) FuzzyResult) []T {
	query = strings.TrimSpace(query)
	if query == "" {
		return append([]T(nil), items...)
	}
	tokens := strings.FieldsFunc(query, func(r rune) bool { return r == '/' || r == ' ' || r == '\t' || r == '\n' })
	type scored struct {
		item  T
		score float64
		order int
	}
	matched := make([]scored, 0, len(items))
	for index, item := range items {
		total, ok := 0.0, true
		for _, token := range tokens {
			result := match(token, text(item))
			if !result.Matches {
				ok = false
				break
			}
			total += result.Score
		}
		if ok {
			matched = append(matched, scored{item: item, score: total, order: index})
		}
	}
	slices.SortStableFunc(matched, func(a, b scored) int { return cmp.Compare(a.score, b.score) })
	result := make([]T, len(matched))
	for index := range matched {
		result[index] = matched[index].item
	}
	return result
}

func fuzzyMatchWithTypos(query, value string) FuzzyResult {
	result := FuzzyMatch(query, value)
	if result.Matches {
		return result
	}

	queryRunes := []rune(strings.ToLower(query))
	valueRunes := []rune(strings.ToLower(value))
	if len(queryRunes) < 3 || len(valueRunes) < 3 || absInt(len(queryRunes)-len(valueRunes)) > 1 {
		return result
	}
	if editDistance(queryRunes, valueRunes) != 1 {
		return result
	}

	prefix := 0
	for prefix < len(queryRunes) && prefix < len(valueRunes) && queryRunes[prefix] == valueRunes[prefix] {
		prefix++
	}
	return FuzzyResult{Matches: true, Score: 125 - float64(prefix)}
}

// editDistance calculates optimal-string-alignment distance, which treats an
// adjacent transposition (for example, "moedl") as one edit.
func editDistance(left, right []rune) int {
	distance := make([][]int, len(left)+1)
	for row := range distance {
		distance[row] = make([]int, len(right)+1)
		distance[row][0] = row
	}
	for column := range distance[0] {
		distance[0][column] = column
	}

	for row := 1; row <= len(left); row++ {
		for column := 1; column <= len(right); column++ {
			cost := 1
			if left[row-1] == right[column-1] {
				cost = 0
			}
			distance[row][column] = min(
				distance[row-1][column]+1,
				distance[row][column-1]+1,
				distance[row-1][column-1]+cost,
			)
			if row > 1 && column > 1 && left[row-1] == right[column-2] && left[row-2] == right[column-1] {
				distance[row][column] = min(distance[row][column], distance[row-2][column-2]+1)
			}
		}
	}
	return distance[len(left)][len(right)]
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
