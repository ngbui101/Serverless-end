package main

import "testing"

func TestParseMinIOEvent(t *testing.T) {
	payload := []byte(`{
		"Records": [
			{
				"s3": {
					"bucket": {"name": "images-raw-serverless"},
					"object": {"key": "folder/my+image%201.png"}
				}
			}
		]
	}`)

	event, err := parseMinIOEvent(payload)
	if err != nil {
		t.Fatalf("parseMinIOEvent returned error: %v", err)
	}
	if event.Bucket != "images-raw-serverless" {
		t.Fatalf("bucket = %q", event.Bucket)
	}
	if event.Key != "folder/my image 1.png" {
		t.Fatalf("key = %q", event.Key)
	}
}

func TestParseMinIOEventRejectsMissingRecord(t *testing.T) {
	_, err := parseMinIOEvent([]byte(`{"Records":[]}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestReplaceExtension(t *testing.T) {
	tests := map[string]string{
		"raw/test.png":  "raw/test.webp",
		"raw/test.jpg":  "raw/test.webp",
		"raw/test":      "raw/test.webp",
		"raw/a.b/c.JPG": "raw/a.b/c.webp",
	}
	for input, want := range tests {
		if got := replaceExtension(input, ".webp"); got != want {
			t.Fatalf("replaceExtension(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseMinIOEndpoint(t *testing.T) {
	endpoint, secure, err := parseMinIOEndpoint("https://minio.example:9000")
	if err != nil {
		t.Fatalf("parseMinIOEndpoint returned error: %v", err)
	}
	if endpoint != "minio.example:9000" || !secure {
		t.Fatalf("endpoint=%q secure=%v", endpoint, secure)
	}
}
