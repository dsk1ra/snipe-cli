// Package guards keeps the tailored CV honest.
//
// Every function here is a DETECTOR working off a vocabulary, and the contract
// that makes that sound is one-sided: a missing entry understates the count, it
// never invents one. A guard that fires wrongly deletes true evidence off a real
// application, so the lists err towards silence.
//
// The repair differs by surface, and the difference is the whole design.
// Experience bullets are REVERTED to the cv.md line they came from, because each
// has a known source and the untailored truth fills the slot. Summaries and
// project blurbs get clause surgery instead: they are synthesised from several
// bullets, have no single line to fall back to, and a shorter true blurb beats a
// longer false one.
package guards

import (
	"regexp"
	"strings"
)

// NamedProducts are the product names a tailored CV has to ground in cv.md.
//
// Seeded from the 9 real violations found across 133 shipped CVs (gcp x3,
// kotlin, golang, dbt, terraform, azure, angular) and extended to the products a
// JD in this corpus is likely to name.
var NamedProducts = []string{
	"aws", "azure", "gcp", "google cloud", "heroku", "digitalocean", "cloudflare", "firebase",
	"vercel", "netlify", "openshift", "cloud foundry", "kotlin", "golang", "scala", "clojure",
	"elixir", "erlang", "haskell", "swift", "objective-c", "perl", "ruby", "php", "c#", "f#",
	"dart", "julia", "groovy", "visual basic", "cobol", "fortran", "lua", "r language", "angular",
	"vue", "svelte", "ember", "backbone", "jquery", "solidjs", "tailwind", "bootstrap",
	"material ui", "sass", "webpack", "vite", "nuxt", "django", "flask", "fastapi", "rails",
	"laravel", "symfony", "phoenix", "spring boot", "quarkus", "micronaut", "nestjs", "gin",
	"echo framework", "asp.net", ".net core", "entity framework", "cassandra", "dynamodb",
	"cockroachdb", "couchbase", "couchdb", "neo4j", "influxdb", "clickhouse", "snowflake",
	"bigquery", "redshift", "databricks", "oracle db", "db2", "mariadb", "firestore",
	"elasticsearch", "opensearch", "timescaledb", "scylladb", "planetscale", "supabase", "kafka",
	"rabbitmq", "pulsar", "nats", "activemq", "sqs", "kinesis", "flink", "spark", "hadoop",
	"airflow", "dagster", "dbt", "beam", "terraform", "pulumi", "ansible", "puppet", "chef",
	"vagrant", "nomad", "consul", "vault", "istio", "linkerd", "helm", "argocd", "flux",
	"cloudformation", "packer", "saltstack", "datadog", "new relic", "splunk", "sentry",
	"grafana", "jaeger", "zipkin", "opentelemetry", "pagerduty", "dynatrace", "appdynamics",
	"circleci", "travis", "teamcity", "bamboo", "gitlab ci", "github actions", "bitbucket",
	"azure devops", "octopus deploy", "pytorch", "tensorflow", "keras", "jax", "scikit-learn",
	"huggingface", "langchain", "llamaindex", "mlflow", "kubeflow", "sagemaker", "vertex ai",
	"weights & biases", "ray", "salesforce", "sap", "workday", "servicenow", "netsuite",
	"hubspot", "shopify", "magento", "contentful", "sanity", "wordpress", "drupal", "twilio",
	"sendgrid", "auth0", "okta", "keycloak", "segment", "snowplow", "looker", "tableau",
	"power bi", "qlik", "metabase", "superset"}

// CasedProducts are names too short for phrase matching, or common enough as
// English words that a lowercase match would fire constantly.
//
// "Go" is the motivating case: it shipped in "Demonstrated ability to deliver
// production systems in Go and Rust" against a CV that names Go nowhere. Case is
// the discriminator — the language is always written "Go", the verb "go" — which
// is why these cannot live in NamedProducts and are matched against the
// original-case text.
var CasedProducts = []string{"Go", "Rust", "Nim", "Zig"}

var phraseNoise = regexp.MustCompile(`[^a-z0-9+#.&-]+`)
var spaceRun = regexp.MustCompile(`\s+`)

// phraseSpace normalises text and guarantees a leading and trailing space, so a
// phrase match cannot straddle a word boundary.
func phraseSpace(s string) string {
	s = phraseNoise.ReplaceAllString(strings.ToLower(s), " ")
	return " " + strings.TrimSpace(spaceRun.ReplaceAllString(s, " ")) + " "
}

func hasPhrase(haystack, phrase string) bool {
	return strings.Contains(haystack, " "+phrase+" ")
}

// ProductFab lists the products text names that cvText does not.
func ProductFab(text, cvText string) []string {
	out, cv := phraseSpace(text), phraseSpace(cvText)
	var bad []string
	for _, p := range NamedProducts {
		if hasPhrase(out, p) && !hasPhrase(cv, p) {
			bad = append(bad, p)
		}
	}
	return bad
}

// CasedFab lists the cased short names text claims that cvText never mentions.
func CasedFab(text, cvText string) []string {
	var bad []string
	for _, p := range CasedProducts {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(p) + `\b`)
		if re.MatchString(text) && !re.MatchString(cvText) {
			bad = append(bad, p)
		}
	}
	return bad
}
