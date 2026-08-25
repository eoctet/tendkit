package downloader

import (
	"slices"
	"testing"
)

func TestMergeDownloaderExtraArgsOverridesAndAppendsApplicationOptions(t *testing.T) {
	for _, test := range []struct {
		name        string
		defaults    []string
		application []string
		want        []string
	}{
		{
			name: "override and append", defaults: []string{"--summary-interval=1", "--console-log-level=notice"},
			application: []string{"--summary-interval=5", "--retry-wait=2"},
			want:        []string{"--summary-interval=5", "--console-log-level=notice", "--retry-wait=2"},
		},
		{
			name: "override transfer options", defaults: []string{"--continue=true", "--split=16", "--max-connection-per-server=4"},
			application: []string{"--continue=false", "--split=1", "--max-connection-per-server=1"},
			want:        []string{"--continue=false", "--split=1", "--max-connection-per-server=1"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := MergeDownloaderExtraArgs(DownloaderAria2, test.defaults, test.application)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("merged arguments = %v, want %v", got, test.want)
			}
		})
	}
}

func TestValidateDownloaderExtraArgsRejectsProgramControlledOptions(t *testing.T) {
	tests := map[DownloaderKind][]string{
		DownloaderAria2: {
			"--dir=/tmp", "--out=artifact", "--allow-overwrite=true", "--auto-file-renaming=false", "--checksum=sha-256=deadbeef",
			"--enable-rpc=true", "--on-download-complete=hook", "--conf-path=config", "--input-file=queue", "--log=aria2.log", "--save-session=session",
		},
		DownloaderCurl: {
			"--output=/tmp/file", "--output-dir=/tmp", "--remote-name=true", "--config=/tmp/curlrc", "--url=https://example.invalid",
			"--write-out=%{json}", "--progress-bar=true", "--no-progress-meter=true", "--stderr=/tmp/log", "--upload-file=/tmp/file",
		},
	}
	for kind, arguments := range tests {
		for _, argument := range arguments {
			if err := ValidateDownloaderExtraArgs(kind, []string{argument}); err == nil {
				t.Fatalf("%s program-controlled option %q was accepted", kind, argument)
			}
		}
	}
}

func TestValidateDownloaderExtraArgsRejectsCurlOptionsBySafetyCategory(t *testing.T) {
	for category, arguments := range map[string][]string{
		"request body or upload":  {"--json={\"name\":\"value\"}", "--data-urlencode=name=value", "--form-string=name=value", "--upload-file=file"},
		"fixed request target":    {"--url-query=name=value", "--request-target=/other", "--path-as-is"},
		"ambient credentials":     {"--netrc", "--netrc-file=/tmp/netrc", "--netrc-optional"},
		"redirect authentication": {"--location-trusted"},
		"side output":             {"--output=/tmp/file", "--dump-header=/tmp/headers", "--trace=/tmp/trace"},
		"connection rewrite":      {"--unix-socket=/tmp/socket", "--abstract-unix-socket=name", "--resolve=example.invalid:443:127.0.0.1", "--connect-to=example.invalid:443:127.0.0.1:443"},
	} {
		t.Run(category, func(t *testing.T) {
			for _, argument := range arguments {
				if err := ValidateDownloaderExtraArgs(DownloaderCurl, []string{argument}); err == nil {
					t.Fatalf("curl unsafe option %q was accepted", argument)
				}
			}
		})
	}
	if err := ValidateDownloaderExtraArgs(DownloaderCurl, []string{"--connect-timeout=15", "--retry=2"}); err != nil {
		t.Fatalf("safe curl options were rejected: %v", err)
	}
}

func TestValidateDownloaderExtraArgsUsesCurlTransferTuningAllowlist(t *testing.T) {
	for _, argument := range []string{
		"--retry=3", "--retry-all-errors", "--retry-delay=2", "--retry-max-time=60",
		"--connect-timeout=10", "--max-time=120", "--speed-limit=1024", "--speed-time=30",
		"--limit-rate=1M", "--parallel", "--parallel-max=4", "--keepalive-time=30",
	} {
		if err := ValidateDownloaderExtraArgs(DownloaderCurl, []string{argument}); err != nil {
			t.Fatalf("safe curl transfer tuning option %q was rejected: %v", argument, err)
		}
	}
	for category, arguments := range map[string][]string{
		"output and metadata":               {"--no-clobber", "--remote-name-all", "--create-file-mode=0600", "--xattr"},
		"credentials and request injection": {"--cookie=/tmp/cookie", "--header=@file", "--proxy-header=@file"},
		"unknown options":                   {"--future-curl-option=value", "--unexpected"},
	} {
		t.Run(category, func(t *testing.T) {
			for _, argument := range arguments {
				if err := ValidateDownloaderExtraArgs(DownloaderCurl, []string{argument}); err == nil {
					t.Fatalf("curl disallowed option %q was accepted", argument)
				}
			}
		})
	}
}

func TestDownloaderKindFromCLIUsesBasename(t *testing.T) {
	for cli, want := range map[string]DownloaderKind{
		"aria2c": DownloaderAria2, "/opt/homebrew/bin/aria2c": DownloaderAria2,
		"curl": DownloaderCurl, "/usr/bin/curl": DownloaderCurl,
	} {
		got, err := DownloaderKindFromCLI(cli)
		if err != nil || got != want {
			t.Fatalf("DownloaderKindFromCLI(%q) = %q, %v; want %q", cli, got, err, want)
		}
	}
	if _, err := DownloaderKindFromCLI("wget"); err == nil {
		t.Fatal("unsupported downloader was accepted")
	}
}

func TestValidateDownloaderExtraArgsRejectsArgumentsForAnotherAdapter(t *testing.T) {
	for _, argument := range []string{
		"--split=16", "--max-connection-per-server=4",
	} {
		if err := ValidateDownloaderExtraArgs(DownloaderCurl, []string{argument}); err == nil {
			t.Fatalf("aria2 option %q was accepted for curl", argument)
		}
	}
}

func TestValidateDownloaderExtraArgsRejectsSplitOptionTokens(t *testing.T) {
	for _, argument := range []string{"--header X-Test: value", "--retry\n3", "https://example.invalid"} {
		if err := ValidateDownloaderExtraArgs(DownloaderCurl, []string{argument}); err == nil {
			t.Fatalf("malformed argument %q was accepted", argument)
		}
	}
}
