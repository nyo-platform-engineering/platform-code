package dbtrace

import (
	"strings"
	"unicode/utf8"

	sqllexer "github.com/DataDog/go-sqllexer"
	"go.opentelemetry.io/otel/attribute"
)

const maxQueryInput = 64 << 10
const maxQueryText = 8 << 10

var queryObfuscator = sqllexer.NewObfuscator()
var queryNormalizer = sqllexer.NewNormalizer(
	sqllexer.WithCollectTables(true),
	sqllexer.WithCollectCommands(true),
	sqllexer.WithKeepIdentifierQuotation(true),
	sqllexer.WithKeepSQLAlias(true),
	sqllexer.WithUppercaseKeywords(true),
)

// queryDetails never falls back to raw SQL. Oversized or unrecognized statements
// remain generic; literals and comments are removed before anything is exported.
// Bound parameters are not passed to this function at all.
func queryDetails(query string) (string, []attribute.KeyValue) {
	if len(query) > maxQueryInput {
		return "postgres query", []attribute.KeyValue{attribute.Bool("aat.db.query_text.omitted", true)}
	}
	// This lexer does not support nested block comments. Fail closed rather than
	// interpreting the remainder of a comment as SQL identifiers.
	if strings.Count(query, "/*") > 1 {
		return "postgres query", nil
	}
	text, meta, err := sqllexer.ObfuscateAndNormalize(query, queryObfuscator, queryNormalizer, sqllexer.WithDBMS(sqllexer.DBMSPostgres))
	if err != nil || meta == nil || strings.TrimSpace(text) == "" || unresolvedDollarQuote(text) {
		return "postgres query", nil
	}
	commands := meta.Commands
	if len(commands) > 3 {
		commands = commands[:3]
	}
	summary := strings.Join(commands, " ")
	if summary == "" {
		summary = "postgres query"
	}
	tables := meta.Tables
	if len(tables) > 3 {
		tables = tables[:3]
	}
	if len(tables) > 0 {
		summary += " " + strings.Join(tables, ", ")
	}
	summary = truncateText(summary, 160)
	attrs := []attribute.KeyValue{attribute.String("db.query.summary", summary), attribute.String("db.query.text", truncateText(text, maxQueryText))}
	return summary, attrs
}

func truncateText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit] + "…"
}

// Closed dollar-quoted literals have already become placeholders. Any remaining
// dollar sign must be a positional bind marker, never an unclosed dollar quote.
func unresolvedDollarQuote(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] != '$' {
			continue
		}
		i++
		if i == len(text) || text[i] < '0' || text[i] > '9' {
			return true
		}
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		if i < len(text) && ((text[i] >= 'a' && text[i] <= 'z') || (text[i] >= 'A' && text[i] <= 'Z') || text[i] == '_') {
			return true
		}
		i--
	}
	return false
}
