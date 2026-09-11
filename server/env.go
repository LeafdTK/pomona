package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Pomona reads a .env before anything else, so credentials can live in a file
// you manage rather than a form you fill in. It looks next to the binary and in
// the data directory; anything already set in the real environment wins, so a
// deployment can override the file without editing it.
//
// Recognised keys:
//
//	POMONA_SLACK_CLIENT_ID
//	POMONA_SLACK_CLIENT_SECRET
func loadEnv(dataDir string) []string {
	loaded := []string{}
	for _, path := range []string{".env", filepath.Join(dataDir, ".env")} {
		file, err := os.Open(path)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimPrefix(line, "export ")

			key, value, found := strings.Cut(line, "=")
			if !found {
				continue
			}
			key = strings.TrimSpace(key)
			value = strings.Trim(strings.TrimSpace(value), `"'`)

			// The real environment beats the file.
			if _, already := os.LookupEnv(key); already {
				continue
			}
			if os.Setenv(key, value) == nil {
				loaded = append(loaded, key)
			}
		}
		file.Close()
	}
	return loaded
}
