package reporter_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cloudflare/pint/internal/checks"
	"github.com/cloudflare/pint/internal/discovery"
	"github.com/cloudflare/pint/internal/git"
	"github.com/cloudflare/pint/internal/parser"
	"github.com/cloudflare/pint/internal/reporter"
	"github.com/rs/zerolog"
)

func TestGithubReporter(t *testing.T) {
	zerolog.SetGlobalLevel(zerolog.FatalLevel)

	type errorCheck func(t *testing.T, err error) error

	type testCaseT struct {
		description  string
		summary      reporter.Summary
		httpHandler  http.Handler
		errorHandler errorCheck
		gitCmd       git.CommandRunner

		owner   string
		repo    string
		token   string
		prNum   int
		timeout time.Duration
	}

	p := parser.NewParser()
	mockRules, _ := p.Parse([]byte(`
- record: target is down
  expr: up == 0
- record: sum errors
  expr: sum(errors) by (job)
`))

	for _, tcase := range []testCaseT{
		{
			description: "timeout errors out",
			owner:       "foo",
			repo:        "bar",
			token:       "something",
			prNum:       123,
			httpHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(1 * time.Second)
				_, _ = w.Write([]byte("OK"))
			}),
			timeout: 100 * time.Millisecond,
			gitCmd: func(args ...string) ([]byte, error) {
				if args[0] == "rev-parse" {
					return []byte("fake-commit-id"), nil
				}
				if args[0] == "blame" {
					content := blameLine("fake-commit-id", 2, "foo.txt", "up == 0")
					return []byte(content), nil
				}
				return nil, nil
			},
			errorHandler: func(t *testing.T, err error) error {
				if err == nil {
					return fmt.Errorf("expected an error")
				}
				if err.Error() != "creating review: context deadline exceeded" {
					return fmt.Errorf("unexpected error")
				}
				return nil
			},
			summary: reporter.Summary{
				Reports: []reporter.Report{
					{
						Path: "foo.txt",
						Rule: mockRules[1],
						Problem: checks.Problem{
							Fragment: "syntax error",
							Lines:    []int{2},
							Reporter: "mock",
							Text:     "syntax error",
							Severity: checks.Fatal,
						},
					},
				},
				FileChanges: discovery.NewFileCommitsFromMap(map[string][]string{"foo.txt": {"fake-commit-id"}}),
			},
		},
		{
			description: "happy path",
			owner:       "foo",
			repo:        "bar",
			token:       "something",
			prNum:       123,
			timeout:     1000 * time.Millisecond,
			errorHandler: func(t *testing.T, err error) error {
				return err
			},
			gitCmd: func(args ...string) ([]byte, error) {
				if args[0] == "rev-parse" {
					return []byte("fake-commit-id"), nil
				}
				if args[0] == "blame" {
					content := blameLine("fake-commit-id", 2, "foo.txt", "up == 0")
					return []byte(content), nil
				}
				return nil, nil
			},
			summary: reporter.Summary{
				Reports: []reporter.Report{
					{
						Path: "foo.txt",
						Rule: mockRules[1],
						Problem: checks.Problem{
							Fragment: "syntax error",
							Lines:    []int{2},
							Reporter: "mock",
							Text:     "syntax error",
							Severity: checks.Fatal,
						},
					},
				},
				FileChanges: discovery.NewFileCommitsFromMap(map[string][]string{"foo.txt": {"fake-commit-id"}}),
			},
		},
	} {
		t.Run(tcase.description, func(t *testing.T) {
			var handler http.Handler
			if tcase.httpHandler != nil {
				handler = tcase.httpHandler
			} else {
				// Handler that checks for token.
				handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					auth := r.Header["Authorization"]
					if len(auth) == 0 {
						w.WriteHeader(500)
						_, _ = w.Write([]byte("No token"))
						t.Fatal("got a request with no token")
						return
					}
					token := auth[0]
					if token != fmt.Sprintf("Bearer %s", tcase.token) {
						w.WriteHeader(500)
						_, _ = w.Write([]byte("Invalid token"))
						t.Fatalf("got a request with invalid token (got %s)", token)
					}
				})
			}
			srv := httptest.NewServer(handler)
			defer srv.Close()
			reporter := reporter.NewGithubReporter(
				&srv.URL,
				&srv.URL,
				tcase.timeout,
				tcase.token,
				tcase.owner,
				tcase.repo,
				tcase.prNum,
				tcase.gitCmd,
			)

			err := reporter.Submit(tcase.summary)
			if e := tcase.errorHandler(t, err); e != nil {
				t.Errorf("error check failure: %s", e)
				return
			}
		})
	}
}
