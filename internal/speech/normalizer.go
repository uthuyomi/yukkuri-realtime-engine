package speech

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

type Normalizer struct {
	whitespacePattern *regexp.Regexp
}

func NewNormalizer() *Normalizer {
	return &Normalizer{
		whitespacePattern: regexp.MustCompile(`\s+`),
	}
}

func (n *Normalizer) Normalize(text string) (string, error) {
	text = strings.TrimSpace(text)

	if text == "" {
		return "", nil
	}

	text = normalizeUnicodeCharacters(text)

	text = n.whitespacePattern.ReplaceAllString(
		text,
		" ",
	)

	text = strings.TrimSpace(text)

	if text == "" {
		return "", nil
	}

	if err := validateSpeechText(text); err != nil {
		return "", err
	}

	return text, nil
}

func normalizeUnicodeCharacters(text string) string {
	var builder strings.Builder

	for _, r := range text {
		switch r {
		case '\r', '\n', '\t':
			builder.WriteRune(' ')

		case '，':
			builder.WriteRune('、')

		case '．':
			builder.WriteRune('。')

		case '！':
			builder.WriteRune('！')

		case '？':
			builder.WriteRune('？')

		case '：':
			builder.WriteRune('、')

		case '；':
			builder.WriteRune('、')

		case '“', '”', '„', '‟':
			builder.WriteRune('"')

		case '‘', '’', '‚', '‛':
			builder.WriteRune('\'')

		case '　':
			builder.WriteRune(' ')

		default:
			builder.WriteRune(r)
		}
	}

	return builder.String()
}

func validateSpeechText(text string) error {
	for _, r := range text {
		if unicode.IsControl(r) {
			return fmt.Errorf(
				"speech text contains unsupported control character U+%04X",
				r,
			)
		}
	}

	return nil
}
