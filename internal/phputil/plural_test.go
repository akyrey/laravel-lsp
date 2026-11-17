package phputil_test

import (
	"testing"

	"github.com/akyrey/laravel-lsp/internal/phputil"
)

func TestPlural(t *testing.T) {
	tests := []struct{ in, want string }{
		{"user", "users"},
		{"post", "posts"},
		{"category", "categories"},
		{"currency", "currencies"},
		{"day", "days"}, // vowel + y keeps the y
		{"status", "statuses"},
		{"address", "addresses"},
		{"box", "boxes"},
		{"quiz", "quizzes"},
		{"match", "matches"},
		{"dish", "dishes"},
		{"person", "people"},
		{"child", "children"},
		{"leaf", "leaves"},
		{"knife", "knives"},
		{"equipment", "equipment"}, // uncountable
		{"analysis", "analyses"},
		{"axis", "axes"},
		{"matrix", "matrices"},
		{"vertex", "vertices"},
		{"index", "indices"},
		{"hero", "heroes"},
		{"potato", "potatoes"},
		{"photo", "photos"},
		{"video", "videos"},
		{"Person", "People"}, // preserves leading capital
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := phputil.Plural(tc.in); got != tc.want {
				t.Errorf("Plural(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TableName mirrors Model::getTable(): Str::snake(Str::pluralStudly(class_basename)),
// which pluralises only the last word.
func TestTableName(t *testing.T) {
	tests := []struct {
		fqn  phputil.FQN
		want string
	}{
		{"App\\Models\\User", "users"},
		{"App\\Models\\SocialUser", "social_users"},
		{"App\\Models\\Category", "categories"},
		{"App\\Models\\BlogPostCategory", "blog_post_categories"},
		{"App\\Models\\Person", "people"},
		{"Transaction", "transactions"},
	}
	for _, tc := range tests {
		t.Run(string(tc.fqn), func(t *testing.T) {
			if got := phputil.TableName(tc.fqn); got != tc.want {
				t.Errorf("TableName(%s) = %q, want %q", tc.fqn, got, tc.want)
			}
		})
	}
}
