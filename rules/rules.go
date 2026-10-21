package rules

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	fberrors "github.com/dawidpiotrowski/filebrowser-ng/v2/errors"
)

// Checker is a Rules checker.
type Checker interface {
	Check(path string) bool
}

// Rule is a allow/disallow rule.
type Rule struct {
	Regex  bool    `json:"regex"`
	Allow  bool    `json:"allow"`
	Path   string  `json:"path"`
	Regexp *Regexp `json:"regexp"`
}

// MatchHidden matches paths with a basename
// that begins with a dot.
func MatchHidden(path string) bool {
	return path != "" && strings.HasPrefix(filepath.Base(path), ".")
}

// Matches matches a path against a rule. When fold is true the comparison is
// case-insensitive: on a case-insensitive filesystem two paths differing only
// in case name the same file, so a rule written for one spelling has to cover
// the others or it can be trivially evaded.
//
// Regex rules are never folded. An admin-authored pattern means what it says,
// and lowering its input would silently change it; use (?i) for those.
func (r *Rule) Matches(path string, fold bool) bool {
	if r.Regex {
		return r.Regexp.MatchString(path)
	}

	rulePath := r.Path
	if fold {
		path = strings.ToLower(path)
		rulePath = strings.ToLower(rulePath)
	}

	if path == rulePath {
		return true
	}

	prefix := rulePath
	if prefix != "/" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	return strings.HasPrefix(path, prefix)
}

// Regexp is a wrapper to the native regexp type where we
// save the raw expression.
type Regexp struct {
	Raw         string `json:"raw"`
	mu          sync.Mutex
	regexp      *regexp.Regexp
	compiledRaw string
}

// MatchString checks if a string matches the regexp.
func (r *Regexp) MatchString(s string) bool {
	compiled, err := r.compile()
	return err == nil && compiled.MatchString(s)
}

func (r *Regexp) compile() (*regexp.Regexp, error) {
	if r == nil {
		return nil, fmt.Errorf("missing regular expression: %w", fberrors.ErrInvalidRequestParams)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.regexp == nil || r.compiledRaw != r.Raw {
		compiled, err := regexp.Compile(r.Raw)
		if err != nil {
			return nil, fmt.Errorf("invalid regular expression (%s): %w", err, fberrors.ErrInvalidRequestParams)
		}
		r.regexp = compiled
		r.compiledRaw = r.Raw
	}
	return r.regexp, nil
}

func (r *Rule) Validate() error {
	if !r.Regex {
		return nil
	}
	_, err := r.Regexp.compile()
	return err
}

// Validate rejects invalid rule sets before they become persistent policy.
func Validate(set []Rule) error {
	for i := range set {
		if err := set[i].Validate(); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
	}
	return nil
}
