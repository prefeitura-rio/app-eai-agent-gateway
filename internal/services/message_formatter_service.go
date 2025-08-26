package services

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
)

// MessageFormatterService implements the MessageFormatterInterface for WhatsApp formatting
type MessageFormatterService struct {
	config *config.Config
	logger *logrus.Logger
}

// NewMessageFormatterService creates a new message formatter service
func NewMessageFormatterService(cfg *config.Config, logger *logrus.Logger) *MessageFormatterService {
	service := &MessageFormatterService{
		config: cfg,
		logger: logger,
	}

	logger.Info("Message formatter service initialized")
	return service
}

// FormatForWhatsApp converts agent response to WhatsApp-compatible format
func (m *MessageFormatterService) FormatForWhatsApp(ctx context.Context, response *models.AgentResponse) (string, error) {
	start := time.Now()

	if response == nil {
		return "", fmt.Errorf("agent response is nil")
	}

	m.logger.WithFields(logrus.Fields{
		"message_id":     response.MessageID,
		"thread_id":      response.ThreadID,
		"content_length": len(response.Content),
	}).Debug("Formatting response for WhatsApp")

	if response.Content == "" {
		m.logger.Warn("Empty content in agent response")
		return "I apologize, but I couldn't generate a response. Please try again.", nil
	}

	// Convert markdown to WhatsApp formatting
	formatted := m.convertMarkdownToWhatsApp(response.Content)

	// Apply character limits and formatting rules
	formatted = m.applyWhatsAppLimits(formatted)

	// Clean up extra whitespace
	formatted = m.cleanupWhitespace(formatted)

	duration := time.Since(start)
	m.logger.WithFields(logrus.Fields{
		"message_id":       response.MessageID,
		"original_length":  len(response.Content),
		"formatted_length": len(formatted),
		"duration_ms":      duration.Milliseconds(),
	}).Debug("WhatsApp formatting completed")

	return formatted, nil
}

// FormatErrorMessage creates a user-friendly error message
func (m *MessageFormatterService) FormatErrorMessage(ctx context.Context, err error) string {
	if err == nil {
		return "An unknown error occurred. Please try again."
	}

	errorMsg := err.Error()

	// Common error patterns and their user-friendly messages
	errorMappings := map[string]string{
		"rate limit":           "I'm currently experiencing high demand. Please wait a moment and try again.",
		"timeout":              "The request took too long to process. Please try again with a shorter message.",
		"context deadline":     "The request took too long to process. Please try again with a shorter message.",
		"connection refused":   "I'm temporarily unavailable. Please try again in a few moments.",
		"service unavailable":  "I'm temporarily unavailable. Please try again in a few moments.",
		"invalid credentials":  "There's a configuration issue. Please contact support.",
		"unauthorized":         "There's an authentication issue. Please contact support.",
		"forbidden":            "Access denied. Please contact support.",
		"not found":            "The requested resource was not found. Please try again.",
		"too large":            "Your message is too long. Please try with a shorter message.",
		"unsupported format":   "The file format you sent is not supported. Please try with a different format.",
		"file size":            "The file you sent is too large. Please try with a smaller file.",
		"invalid url":          "The link you provided is not accessible. Please check the link and try again.",
		"transcription failed": "I couldn't process your audio message. Please try recording it again or send a text message.",
		"formatting failed":    "I had trouble formatting the response. Here's the raw content: ",
	}

	// Check for known error patterns
	lowerError := strings.ToLower(errorMsg)
	for pattern, friendlyMsg := range errorMappings {
		if strings.Contains(lowerError, pattern) {
			m.logger.WithFields(logrus.Fields{
				"original_error": errorMsg,
				"pattern":        pattern,
				"friendly_msg":   friendlyMsg,
			}).Debug("Mapped error to user-friendly message")
			return friendlyMsg
		}
	}

	// For unknown errors, provide a generic friendly message
	m.logger.WithField("original_error", errorMsg).Debug("Using generic error message")
	return "I encountered an unexpected issue. Please try again, and if the problem persists, contact support."
}

// ValidateMessageContent validates message content before processing
func (m *MessageFormatterService) ValidateMessageContent(content string) error {
	if content == "" {
		return fmt.Errorf("message content cannot be empty")
	}

	// Check content length
	maxLength := 10000 // Default max length
	if m.config != nil && m.config.GoogleAgentEngine.MaxMessageLength > 0 {
		maxLength = m.config.GoogleAgentEngine.MaxMessageLength
	}

	if len(content) > maxLength {
		return fmt.Errorf("message content exceeds maximum length of %d characters", maxLength)
	}

	// Check for potentially harmful content patterns
	suspiciousPatterns := []string{
		`<script[^>]*>`,
		`javascript:`,
		`data:text/html`,
		`vbscript:`,
		`on\w+\s*=`,
	}

	for _, pattern := range suspiciousPatterns {
		matched, err := regexp.MatchString(`(?i)`+pattern, content)
		if err != nil {
			m.logger.WithError(err).WithField("pattern", pattern).Warn("Error checking suspicious pattern")
			continue
		}
		if matched {
			return fmt.Errorf("message content contains potentially harmful script")
		}
	}

	return nil
}

// convertMarkdownToWhatsApp converts markdown syntax to WhatsApp formatting
// Based on sophisticated Python markdown_to_whatsapp function with robust order-of-operations
func (m *MessageFormatterService) convertMarkdownToWhatsApp(content string) string {
	// 0. Normalize newlines to prevent issues with \r\n
	convertedText := strings.ReplaceAll(content, "\r\n", "\n")

	// 1. PRESERVATION STAGE
	var codeBlocks []string
	codeBlockRegex := regexp.MustCompile(`(?s)` + "`" + `{3}[\s\S]*?` + "`" + `{3}|` + "`" + `[^` + "`" + `\n]+` + "`")
	convertedText = codeBlockRegex.ReplaceAllStringFunc(convertedText, func(match string) string {
		codeBlocks = append(codeBlocks, match)
		return fmt.Sprintf("¤C%d¤", len(codeBlocks)-1)
	})

	// Protect list markers before they can be confused with italics
	listMarkerRegex := regexp.MustCompile(`(?m)^(\s*)\*\s`)
	convertedText = listMarkerRegex.ReplaceAllString(convertedText, "$1¤L¤ ")

	// 2. DATA EXTRACTION & INLINE CONVERSIONS
	footnoteDefs := make(map[string]string)

	// Collect footnote definitions
	footnoteBlockPattern := regexp.MustCompile(`(?m)(?:\n^\[\^[^\]]+\]:.*$)+`)
	footnoteBlocks := footnoteBlockPattern.FindAllString(convertedText, -1)
	for _, block := range footnoteBlocks {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		for _, line := range lines {
			footnoteDefRegex := regexp.MustCompile(`^\[\^([^\]]+)\]:\s*(.*)$`)
			if matches := footnoteDefRegex.FindStringSubmatch(line); len(matches) == 3 {
				footnoteDefs[matches[1]] = strings.TrimSpace(matches[2])
			}
		}
	}
	convertedText = footnoteBlockPattern.ReplaceAllString(convertedText, "")

	// Run image conversion BEFORE link conversion
	imageRegex := regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	convertedText = imageRegex.ReplaceAllString(convertedText, "[Image: $1]")

	linkRegex := regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)
	convertedText = linkRegex.ReplaceAllString(convertedText, "$2")

	// 3. INLINE FORMATTING (using temporary placeholders)
	// The order is critical: multi-character markers first.

	// Handle strikethrough
	strikethroughRegex := regexp.MustCompile(`(?s)~~(.*?)~~`)
	convertedText = strikethroughRegex.ReplaceAllString(convertedText, "<s>$1</s>")

	// Handle triple asterisk pattern (***text***) for bold+italic
	boldItalicRegex := regexp.MustCompile(`(?s)\*\*\*(.*?)\*\*\*`)
	convertedText = boldItalicRegex.ReplaceAllString(convertedText, "<i><b>$1</b></i>")

	// Handle bold patterns
	boldRegex1 := regexp.MustCompile(`(?s)\*\*(.*?)\*\*`)
	convertedText = boldRegex1.ReplaceAllString(convertedText, "<b>$1</b>")

	boldRegex2 := regexp.MustCompile(`(?s)__(.*?)__`)
	convertedText = boldRegex2.ReplaceAllString(convertedText, "<b>$1</b>")

	// Handle italics - simplified patterns since Go doesn't support lookbehind/lookahead
	// Process single asterisk italics (avoiding conflicts with already processed bold)
	italicRegex1 := regexp.MustCompile(`(?s)\*([^\*\n]+?)\*`)
	convertedText = italicRegex1.ReplaceAllString(convertedText, "<i>$1</i>")

	// Process single underscore italics (avoiding conflicts with already processed bold)
	italicRegex2 := regexp.MustCompile(`(?s)_([^_\n]+?)_`)
	convertedText = italicRegex2.ReplaceAllString(convertedText, "<i>$1</i>")

	// Convert placeholders to final WhatsApp format
	convertedText = strings.ReplaceAll(convertedText, "<s>", "~")
	convertedText = strings.ReplaceAll(convertedText, "</s>", "~")
	convertedText = strings.ReplaceAll(convertedText, "<b>", "*")
	convertedText = strings.ReplaceAll(convertedText, "</b>", "*")
	convertedText = strings.ReplaceAll(convertedText, "<i>", "_")
	convertedText = strings.ReplaceAll(convertedText, "</i>", "_")

	// 4. BLOCK-LEVEL CONVERSIONS
	// Add a newline after headers to ensure separation
	headerRegex := regexp.MustCompile(`(?m)^\s*#+\s+(.+?)\s*#*$`)
	convertedText = headerRegex.ReplaceAllString(convertedText, "*$1*\n")

	// Replace horizontal rules with a guaranteed paragraph break
	hrRegex := regexp.MustCompile(`(?m)^\s*[-*_]{3,}\s*$`)
	convertedText = hrRegex.ReplaceAllString(convertedText, "\n")

	// Handle tables with proper formatting
	tableRegex := regexp.MustCompile(`(?m)(?:^\|.*\|\n?)+`)
	convertedText = tableRegex.ReplaceAllStringFunc(convertedText, m.convertTable)

	// Handle task lists
	taskListRegex := regexp.MustCompile(`(?m)^(\s*[-+]|¤L¤)\s*\[[xX ]\]\s+`)
	convertedText = taskListRegex.ReplaceAllString(convertedText, "$1 ")

	// Handle footnotes
	footnoteMap := make(map[string]int)
	footnoteCounter := 1

	footnoteMarkerRegex := regexp.MustCompile(`\[\^([^\]]+)\]`)
	convertedText = footnoteMarkerRegex.ReplaceAllStringFunc(convertedText, func(match string) string {
		matches := footnoteMarkerRegex.FindStringSubmatch(match)
		if len(matches) < 2 {
			return ""
		}
		key := matches[1]
		if _, exists := footnoteDefs[key]; exists {
			if _, mapped := footnoteMap[key]; !mapped {
				footnoteMap[key] = footnoteCounter
				footnoteCounter++
			}
			return fmt.Sprintf("[%d]", footnoteMap[key])
		}
		return ""
	})

	// 5. RESTORATION & FINAL CLEANUP
	convertedText = strings.ReplaceAll(convertedText, "¤L¤ ", "* ")

	// Normalize list spacing to ensure single space after asterisk
	listSpacingRegex := regexp.MustCompile(`(?m)^\*\s+`)
	convertedText = listSpacingRegex.ReplaceAllString(convertedText, "* ")

	// Restore code blocks
	for i, codeBlock := range codeBlocks {
		convertedText = strings.ReplaceAll(convertedText, fmt.Sprintf("¤C%d¤", i), codeBlock)
	}

	// Clean up specific escaped quote patterns
	escapedQuoteRegex := regexp.MustCompile(`\{r'\\\"(.*?)\\\"\'\}`)
	convertedText = escapedQuoteRegex.ReplaceAllString(convertedText, `"$1"`)

	// Convert double quotes to single quotes
	convertedText = strings.ReplaceAll(convertedText, `"`, "'")

	// Clean up excess newlines
	excessNewlineRegex := regexp.MustCompile(`\n{3,}`)
	convertedText = excessNewlineRegex.ReplaceAllString(convertedText, "\n\n")
	convertedText = strings.TrimSpace(convertedText)

	// Append footnotes if any were found
	if len(footnoteMap) > 0 {
		convertedText += "\n\n---\n_Notas:_\n"

		// Sort footnotes by their number
		type footnoteEntry struct {
			key string
			num int
		}
		var sortedFootnotes []footnoteEntry
		for key, num := range footnoteMap {
			sortedFootnotes = append(sortedFootnotes, footnoteEntry{key, num})
		}

		// Sort by number
		for i := 0; i < len(sortedFootnotes)-1; i++ {
			for j := i + 1; j < len(sortedFootnotes); j++ {
				if sortedFootnotes[i].num > sortedFootnotes[j].num {
					sortedFootnotes[i], sortedFootnotes[j] = sortedFootnotes[j], sortedFootnotes[i]
				}
			}
		}

		for _, footnote := range sortedFootnotes {
			if def, exists := footnoteDefs[footnote.key]; exists {
				convertedText += fmt.Sprintf("[%d] %s\n", footnote.num, def)
			}
		}
		convertedText = strings.TrimRight(convertedText, "\n")
	}

	return convertedText
}

// convertTable converts markdown tables to monospace-formatted tables
func (m *MessageFormatterService) convertTable(match string) string {
	tableText := strings.TrimSpace(match)
	lines := strings.Split(tableText, "\n")

	var rows [][]string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "|") && !regexp.MustCompile(`^[\s|: -]+$`).MatchString(line) {
			parts := strings.Split(line, "|")
			var cells []string
			for _, part := range parts {
				if trimmed := strings.TrimSpace(part); trimmed != "" {
					cells = append(cells, trimmed)
				}
			}
			if len(cells) > 0 {
				rows = append(rows, cells)
			}
		}
	}

	if len(rows) == 0 {
		return ""
	}

	// Calculate maximum number of columns
	numColumns := 0
	for _, row := range rows {
		if len(row) > numColumns {
			numColumns = len(row)
		}
	}

	// Calculate column widths
	columnWidths := make([]int, numColumns)
	for _, row := range rows {
		for i, cell := range row {
			if i < numColumns && len(cell) > columnWidths[i] {
				columnWidths[i] = len(cell)
			}
		}
	}

	// Format the table
	var formattedTable strings.Builder
	for _, row := range rows {
		var formattedCells []string
		for j := 0; j < numColumns; j++ {
			cellText := ""
			if j < len(row) {
				cellText = row[j]
			}
			// Pad to column width
			for len(cellText) < columnWidths[j] {
				cellText += " "
			}
			formattedCells = append(formattedCells, cellText)
		}
		formattedTable.WriteString(strings.Join(formattedCells, " | "))
		formattedTable.WriteString("\n")
	}

	// Return with guaranteed spacing and monospace formatting
	return fmt.Sprintf("\n\n```%s```\n\n", strings.TrimSpace(formattedTable.String()))
}

// applyWhatsAppLimits applies WhatsApp message limits and formatting rules
func (m *MessageFormatterService) applyWhatsAppLimits(content string) string {
	// WhatsApp has a 4096 character limit per message
	const maxWhatsAppLength = 4096

	if len(content) <= maxWhatsAppLength {
		return content
	}

	// If content is too long, truncate and add continuation message
	truncated := content[:maxWhatsAppLength-100] // Leave space for continuation message

	// Try to truncate at a sentence boundary
	sentences := []string{".", "!", "?", "\n"}
	bestCutoff := len(truncated)

	for _, delimiter := range sentences {
		if lastIndex := strings.LastIndex(truncated, delimiter); lastIndex > len(truncated)-200 {
			bestCutoff = lastIndex + 1
			break
		}
	}

	truncated = content[:bestCutoff]
	truncated = strings.TrimSpace(truncated)

	// Add continuation message
	truncated += "\n\n_[Message truncated due to length. Please ask me to continue if you need more information.]_"

	m.logger.WithFields(logrus.Fields{
		"original_length":  len(content),
		"truncated_length": len(truncated),
		"cutoff_position":  bestCutoff,
	}).Info("Message truncated for WhatsApp limits")

	return truncated
}

// cleanupWhitespace removes excessive whitespace and normalizes line breaks
func (m *MessageFormatterService) cleanupWhitespace(content string) string {
	// Remove excessive blank lines (more than 2 consecutive newlines)
	multipleNewlines := regexp.MustCompile(`\n{3,}`)
	content = multipleNewlines.ReplaceAllString(content, "\n\n")

	// Remove trailing whitespace from each line
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	content = strings.Join(lines, "\n")

	// Remove leading and trailing whitespace from the entire content
	content = strings.TrimSpace(content)

	return content
}

// GetFormattingStats returns statistics about the formatting process
func (m *MessageFormatterService) GetFormattingStats(original, formatted string) map[string]interface{} {
	return map[string]interface{}{
		"original_length":   len(original),
		"formatted_length":  len(formatted),
		"compression_ratio": float64(len(formatted)) / float64(len(original)),
		"original_lines":    len(strings.Split(original, "\n")),
		"formatted_lines":   len(strings.Split(formatted, "\n")),
		"markdown_patterns": m.countMarkdownPatterns(original),
		"whatsapp_patterns": m.countWhatsAppPatterns(formatted),
	}
}

// countMarkdownPatterns counts markdown formatting patterns in text
func (m *MessageFormatterService) countMarkdownPatterns(text string) map[string]int {
	counts := make(map[string]int)
	workingText := text

	// Process in order to avoid double-counting overlapping patterns

	// 1. Code blocks first (preserve content)
	codeBlockRegex := regexp.MustCompile("```[\\s\\S]*?```")
	codeBlocks := codeBlockRegex.FindAllString(workingText, -1)
	counts["code_blocks"] = len(codeBlocks)
	workingText = codeBlockRegex.ReplaceAllString(workingText, "CODEBLOCK")

	// 2. Inline code
	inlineCodeRegex := regexp.MustCompile("`[^`\n]+`")
	inlineCode := inlineCodeRegex.FindAllString(workingText, -1)
	counts["inline_code"] = len(inlineCode)
	workingText = inlineCodeRegex.ReplaceAllString(workingText, "INLINE")

	// 3. Bold (double markers first)
	boldRegex := regexp.MustCompile(`\*\*[^*]+\*\*|__[^_]+__`)
	bold := boldRegex.FindAllString(workingText, -1)
	counts["bold"] = len(bold)
	workingText = boldRegex.ReplaceAllString(workingText, "BOLD")

	// 4. Strikethrough
	strikethroughRegex := regexp.MustCompile(`~~[^~]+~~`)
	strikethrough := strikethroughRegex.FindAllString(workingText, -1)
	counts["strikethrough"] = len(strikethrough)
	workingText = strikethroughRegex.ReplaceAllString(workingText, "STRIKE")

	// 5. Italic (single markers, after bold is removed)
	italicRegex := regexp.MustCompile(`\*[^*\n]+\*|_[^_\n]+_`)
	italic := italicRegex.FindAllString(workingText, -1)
	counts["italic"] = len(italic)
	workingText = italicRegex.ReplaceAllString(workingText, "ITALIC")

	// 6. Links
	linkRegex := regexp.MustCompile(`\[[^\]]+\]\([^)]+\)`)
	links := linkRegex.FindAllString(workingText, -1)
	counts["links"] = len(links)
	workingText = linkRegex.ReplaceAllString(workingText, "LINK")

	// 7. Headers
	headerRegex := regexp.MustCompile(`(?m)^#{1,6}\s*.+$`)
	headers := headerRegex.FindAllString(workingText, -1)
	counts["headers"] = len(headers)

	// 8. Lists
	listRegex := regexp.MustCompile(`(?m)^[\s]*[-*+]\s+|^\d+\.\s+`)
	lists := listRegex.FindAllString(workingText, -1)
	counts["lists"] = len(lists)

	return counts
}

// countWhatsAppPatterns counts WhatsApp formatting patterns in text
func (m *MessageFormatterService) countWhatsAppPatterns(text string) map[string]int {
	patterns := map[string]*regexp.Regexp{
		"bold":          regexp.MustCompile(`\*[^*\n]+\*`),
		"italic":        regexp.MustCompile(`_[^_\n]+_`),
		"strikethrough": regexp.MustCompile(`~[^~\n]+~`),
		"code":          regexp.MustCompile("```[\\s\\S]*?```"),
		"bullets":       regexp.MustCompile(`[•◦]`), // Just count the bullet characters themselves
	}

	counts := make(map[string]int)
	for name, regex := range patterns {
		matches := regex.FindAllString(text, -1)
		counts[name] = len(matches)
	}

	return counts
}
