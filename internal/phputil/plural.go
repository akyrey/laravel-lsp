package phputil

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// irregularPlurals and uncountables cover the English words that most often
// name Eloquent models. Laravel delegates to doctrine/inflector, whose rule
// set is far larger; models with exotic names should declare $table, which
// always takes precedence over this convention.
var irregularPlurals = map[string]string{
	"person": "people", "man": "men", "woman": "women", "child": "children",
	"mouse": "mice", "goose": "geese", "tooth": "teeth", "foot": "feet",
	"ox": "oxen", "leaf": "leaves", "knife": "knives", "life": "lives",
	"wife": "wives", "half": "halves", "wolf": "wolves", "shelf": "shelves",
	"criterion": "criteria",
}

var uncountables = map[string]bool{
	"equipment": true, "information": true, "rice": true, "money": true,
	"species": true, "series": true, "fish": true, "sheep": true,
	"news": true, "feedback": true, "metadata": true, "media": true,
}

// Plural returns the English plural of a single lowercase or capitalised
// word, approximating Illuminate\Support\Str::plural for common nouns.
func Plural(word string) string {
	if word == "" {
		return ""
	}
	lower := strings.ToLower(word)
	if uncountables[lower] {
		return word
	}
	plural, ok := irregularPlurals[lower]
	if !ok {
		plural = regularPlural(lower)
	}
	return matchLeadingCase(word, plural)
}

// oesWords take "-oes" (doctrine/inflector's list); other "-o" words take "-s".
var oesWords = map[string]bool{
	"buffalo": true, "tomato": true, "potato": true, "echo": true, "hero": true, "veto": true,
}

// icesStems are the Latin "-ix"/"-ex" stems doctrine/inflector pluralises to "-ices".
var icesStems = map[string]bool{"matr": true, "vert": true, "ind": true}

// regularPlural applies the suffix rules for a lowercase word.
func regularPlural(w string) string {
	if n := len(w); n > 2 && (strings.HasSuffix(w, "ix") || strings.HasSuffix(w, "ex")) && icesStems[w[:n-2]] {
		return w[:n-2] + "ices" // matrix → matrices, index → indices
	}
	switch {
	case oesWords[w]:
		return w + "es"
	case strings.HasSuffix(w, "is") && len(w) > 3:
		return w[:len(w)-2] + "es" // analysis → analyses
	case strings.HasSuffix(w, "z") && !strings.HasSuffix(w, "zz"):
		return w + "zes" // quiz → quizzes
	case strings.HasSuffix(w, "s"), strings.HasSuffix(w, "x"), strings.HasSuffix(w, "z"),
		strings.HasSuffix(w, "ch"), strings.HasSuffix(w, "sh"):
		return w + "es"
	case strings.HasSuffix(w, "y") && len(w) > 1 && !isVowel(w[len(w)-2]):
		return w[:len(w)-1] + "ies"
	}
	return w + "s"
}

func isVowel(b byte) bool { return strings.IndexByte("aeiou", b) >= 0 }

// matchLeadingCase capitalises plural's first letter when original's is.
func matchLeadingCase(original, plural string) string {
	first, _ := utf8.DecodeRuneInString(original)
	if !unicode.IsUpper(first) {
		return plural
	}
	return ucfirst(plural)
}

// TableName returns the table Eloquent infers for a model class without an
// explicit $table: Str::snake(Str::pluralStudly(class_basename($model))).
// Only the last word is pluralised ("SocialUser" → "social_users").
func TableName(class FQN) string {
	short := string(class)
	if i := strings.LastIndex(short, "\\"); i >= 0 {
		short = short[i+1:]
	}
	snake := Snake(short)
	head, last := "", snake
	if i := strings.LastIndex(snake, "_"); i >= 0 {
		head, last = snake[:i+1], snake[i+1:]
	}
	return head + Plural(last)
}
