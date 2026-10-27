package config

// Example is the starter config written by `cbz config init`. It matches
// config.example.json at the repository root (TestExampleMatchesRepoFile), and
// every value is the built-in default except the credentials, which show the
// shape: secrets are referenced by file, never written inline.
const Example = `{
  "providers": {
    "order": ["metron", "comicvine"],
    "prefer": {},
    "cache_ttl": "24h"
  },
  "rename": {
    "template": "{Series}[ ({Year})] #{Number:3}"
  },
  "credentials": {
    "metron": {
      "username": "YOUR_METRON_USERNAME",
      "password_file": "~/.config/cbz/metron-password"
    },
    "comicvine": {
      "api_key_file": "~/.config/cbz/comicvine-api-key"
    }
  }
}
`
