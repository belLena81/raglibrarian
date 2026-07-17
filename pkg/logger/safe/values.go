// Package safe contains transport-neutral, bounded values permitted in fixed
// activity-log templates. It deliberately has no dependency on Zap.
package safe

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const invalidEmail = "invalid-email"

type MaskedEmail string

func (email MaskedEmail) String() string {
	value := string(email)
	if containsControl(value) || !utf8.ValidString(value) {
		return invalidEmail
	}
	parts := strings.Split(value, "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return invalidEmail
	}
	labels := strings.Split(parts[1], ".")
	if len(labels) < 2 || labels[len(labels)-1] == "" {
		return invalidEmail
	}
	for _, label := range labels {
		if label == "" || strings.ContainsAny(label, " @") {
			return invalidEmail
		}
	}
	maskedLabels := make([]string, len(labels))
	for index, label := range labels[:len(labels)-1] {
		maskedLabels[index] = firstRune(label) + "***"
	}
	maskedLabels[len(labels)-1] = labels[len(labels)-1]
	return firstRune(parts[0]) + "***@" + strings.Join(maskedLabels, ".")
}

func firstRune(value string) string {
	for _, character := range value {
		return string(character)
	}
	return ""
}

type BookSummary struct {
	ID, Title, Author string
	Year              int
	Status            string
}

func (book BookSummary) String() string {
	return fmt.Sprintf("book id=%q title=%q author=%q year=%d status=%q", oneLine(book.ID, 128), oneLine(book.Title, 256), oneLine(book.Author, 256), book.Year, oneLine(book.Status, 32))
}

func oneLine(value string, maximum int) string {
	value = strings.ToValidUTF8(value, "?")
	value = strings.Map(func(character rune) rune {
		if unsafeRune(character) {
			return '?'
		}
		return character
	}, value)
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	return string([]rune(value)[:maximum])
}

func containsControl(value string) bool {
	for _, character := range value {
		if unsafeRune(character) {
			return true
		}
	}
	return false
}

func unsafeRune(character rune) bool {
	return unicode.IsControl(character) || unicode.Is(unicode.Cf, character) || character == '\u2028' || character == '\u2029'
}
