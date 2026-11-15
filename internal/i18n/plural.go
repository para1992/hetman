package i18n

// Plural categories, named as in the Unicode CLDR plural rules.
const (
	Zero  = "zero"
	One   = "one"
	Two   = "two"
	Few   = "few"
	Many  = "many"
	Other = "other"
)

// pluralRule picks the category of a whole number for one language.
type pluralRule struct {
	categories []string // every category the rule gives for whole numbers
	pick       func(n int64) string
}

// rules are the plural rules of the built-in languages for whole numbers,
// after the CLDR plural rules. A language without a rule uses the English
// one.
var rules = map[string]pluralRule{
	"ar": {[]string{Zero, One, Two, Few, Many, Other}, func(n int64) string {
		m := n % 100
		switch {
		case n == 0:
			return Zero
		case n == 1:
			return One
		case n == 2:
			return Two
		case m >= 3 && m <= 10:
			return Few
		case m >= 11 && m <= 99:
			return Many
		}
		return Other
	}},
	"en": {[]string{One, Other}, oneIsOne},
	"es": {[]string{One, Other}, oneIsOne},
	"hi": {[]string{One, Other}, func(n int64) string {
		if n == 0 || n == 1 {
			return One
		}
		return Other
	}},
	"uk": {[]string{One, Few, Many}, func(n int64) string {
		d, m := n%10, n%100
		switch {
		case d == 1 && m != 11:
			return One
		case d >= 2 && d <= 4 && (m < 12 || m > 14):
			return Few
		}
		return Many
	}},
	"zh": {[]string{Other}, func(int64) string { return Other }},
}

func oneIsOne(n int64) string {
	if n == 1 {
		return One
	}
	return Other
}

func ruleOf(lang string) pluralRule {
	if r, ok := rules[lang]; ok {
		return r
	}
	return rules["en"]
}

// PluralCategory is the plural category of n in a language.
func PluralCategory(lang string, n int64) string {
	if n < 0 {
		n = -n
	}
	return ruleOf(lang).pick(n)
}

// PluralCategories lists the categories a language's rule gives for whole
// numbers: the forms a plural message of that language must have.
func PluralCategories(lang string) []string {
	return append([]string{}, ruleOf(lang).categories...)
}
