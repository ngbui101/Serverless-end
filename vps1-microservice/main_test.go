package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestParseMinIOEvent(t *testing.T) {
	payload := []byte(`{
		"Records": [
			{
				"s3": {
					"bucket": {"name": "images-raw-classic"},
					"object": {"key": "classic-smoke%2F42%2Fmy+image%201.JPG"}
				}
			}
		]
	}`)

	event, err := parseMinIOEvent(payload)
	if err != nil {
		t.Fatalf("parseMinIOEvent returned error: %v", err)
	}
	if event.Bucket != "images-raw-classic" {
		t.Fatalf("bucket = %q", event.Bucket)
	}
	if event.Key != "classic-smoke/42/my image 1.JPG" {
		t.Fatalf("key = %q", event.Key)
	}
}

func TestParseMinIOEventRejectsInvalidPayloads(t *testing.T) {
	tests := map[string]string{
		"invalid json":   `{`,
		"empty records":  `{"Records":[]}`,
		"missing bucket": `{"Records":[{"s3":{"bucket":{"name":""},"object":{"key":"a.png"}}}]}`,
		"missing key":    `{"Records":[{"s3":{"bucket":{"name":"images-raw-classic"},"object":{"key":""}}}]}`,
		"bad url escape": `{"Records":[{"s3":{"bucket":{"name":"images-raw-classic"},"object":{"key":"bad%ZZ.png"}}}]}`,
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseMinIOEvent([]byte(payload)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestWrongBucketIsParseableForHandlerToSkip(t *testing.T) {
	event, err := parseMinIOEvent([]byte(`{"Records":[{"s3":{"bucket":{"name":"images-raw-serverless"},"object":{"key":"x.png"}}}]}`))
	if err != nil {
		t.Fatalf("parseMinIOEvent returned error: %v", err)
	}
	if event.Bucket == "images-raw-classic" {
		t.Fatal("expected non-classic bucket")
	}
}

func TestReplaceExtension(t *testing.T) {
	tests := map[string]string{
		"raw/test.png":  "raw/test.webp",
		"raw/test.jpg":  "raw/test.webp",
		"raw/test.jpeg": "raw/test.webp",
		"raw/test":      "raw/test.webp",
		"raw/a.b/c.JPG": "raw/a.b/c.webp",
	}
	for input, want := range tests {
		if got := replaceExtension(input, ".webp"); got != want {
			t.Fatalf("replaceExtension(%q) = %q, want %q", input, got, want)
		}
	}
}


func TestPostgresDSNFromSpringURL(t *testing.T) {
	dsn, err := postgresDSNFromSpring("jdbc:postgresql://db.example:5544/benchmark?sslmode=require", "spring_user", "secret/pass", "disable")
	if err != nil {
		t.Fatalf("postgresDSNFromSpring returned error: %v", err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	if parsed.Scheme != "postgres" || parsed.Host != "db.example:5544" || parsed.Path != "/benchmark" {
		t.Fatalf("unexpected dsn: %s", dsn)
	}
	if got := parsed.Query().Get("sslmode"); got != "require" {
		t.Fatalf("sslmode = %q", got)
	}
	if got := parsed.User.Username(); got != "spring_user" {
		t.Fatalf("user = %q", got)
	}
	pass, ok := parsed.User.Password()
	if !ok || pass != "secret/pass" {
		t.Fatalf("password = %q ok=%v", pass, ok)
	}
}

func TestPostgresDSNFromSpringURLDefaultPortAndSSLMode(t *testing.T) {
	dsn, err := postgresDSNFromSpring("jdbc:postgresql://db.internal/benchmark", "user", "pass", "disable")
	if err != nil {
		t.Fatalf("postgresDSNFromSpring returned error: %v", err)
	}
	if !strings.Contains(dsn, "db.internal:5432") {
		t.Fatalf("dsn does not contain default port: %s", dsn)
	}
	if !strings.Contains(dsn, "sslmode=disable") {
		t.Fatalf("dsn does not contain default sslmode: %s", dsn)
	}
}
