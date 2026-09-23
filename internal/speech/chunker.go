package speech

import (
	"strings"
	"sync"
	"unicode/utf8"
)

type ChunkerConfig struct {
	SoftLimit int
	HardLimit int
}

type Chunker struct {
	mu sync.Mutex

	buffer strings.Builder

	softLimit int
	hardLimit int
}

func NewChunker(config ChunkerConfig) *Chunker {
	softLimit := config.SoftLimit
	if softLimit <= 0 {
		softLimit = 40
	}

	hardLimit := config.HardLimit
	if hardLimit <= 0 {
		hardLimit = 80
	}

	if hardLimit < softLimit {
		hardLimit = softLimit
	}

	return &Chunker{
		softLimit: softLimit,
		hardLimit: hardLimit,
	}
}

func (c *Chunker) Push(delta string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if delta == "" {
		return nil
	}

	c.buffer.WriteString(delta)

	return c.extract(false)
}

func (c *Chunker) Flush() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.extract(true)
}

func (c *Chunker) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.buffer.Reset()
}

func (c *Chunker) extract(flush bool) []string {
	text := c.buffer.String()

	if text == "" {
		return nil
	}

	var chunks []string

	for {
		cut := findChunkBoundary(
			text,
			c.softLimit,
			c.hardLimit,
			flush,
		)

		if cut <= 0 {
			break
		}

		chunk := strings.TrimSpace(text[:cut])

		if chunk != "" {
			chunks = append(chunks, chunk)
		}

		text = strings.TrimLeft(
			text[cut:],
			" \t\r\n",
		)

		if text == "" {
			break
		}

		if !flush &&
			utf8.RuneCountInString(text) < c.softLimit {
			break
		}
	}

	c.buffer.Reset()
	c.buffer.WriteString(text)

	return chunks
}

func findChunkBoundary(
	text string,
	softLimit int,
	hardLimit int,
	flush bool,
) int {
	type boundary struct {
		byteIndex int
		runeIndex int
		priority  int
	}

	var best boundary

	runeCount := 0

	for byteIndex, r := range text {
		runeCount++

		priority := boundaryPriority(r)

		if priority == 0 {
			if runeCount >= hardLimit {
				if best.byteIndex > 0 {
					return best.byteIndex
				}

				_, size := utf8.DecodeRuneInString(
					text[byteIndex:],
				)

				return byteIndex + size
			}

			continue
		}

		_, size := utf8.DecodeRuneInString(
			text[byteIndex:],
		)

		end := byteIndex + size

		if runeCount >= softLimit {
			return end
		}

		if priority > best.priority {
			best = boundary{
				byteIndex: end,
				runeIndex: runeCount,
				priority:  priority,
			}
		} else if priority == best.priority {
			best = boundary{
				byteIndex: end,
				runeIndex: runeCount,
				priority:  priority,
			}
		}
	}

	if flush {
		return len(text)
	}

	return 0
}

func boundaryPriority(r rune) int {
	switch r {
	case '。', '！', '？', '!', '?':
		return 3

	case '、', ',', '，', ';', '；', ':', '：':
		return 2

	case '\n':
		return 3

	default:
		return 0
	}
}
